package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

const (
	cmdSPIWrite     = 0x80 // wIndex 1 ends the SPI transaction, 0 keeps it open
	cmdSPIWriteRead = 0x81 // wValue = bytes to clock in afterwards
	cmdSPIFetch     = 0x82 // returns the bytes clocked in by 0x81
	cmdSPIReady     = 0x83 // 1 byte, non-zero when the SPI engine is idle
	cmdISPEnter     = 0x84
	cmdISPExit      = 0x85

	spiWriteStatus = 0x01
	spiPageProgram = 0x02
	spiRead        = 0x03
	spiReadStatus  = 0x05
	spiWriteEnable = 0x06
	spiSectorErase = 0x20
	spiReadID      = 0x9f

	statusBusy    = 0x01
	statusProtect = 0xfc

	readChunk  = 0x40 // the vendor tool reads 64 bytes per transaction
	sectorSize = 0x1000
)

// enterISP stops the firmware and hands the SPI flash to the host.
func (d *dev) enterISP() error {
	if _, err := d.control(reqOut, cmdISPEnter, 0x5354, 0x2080, nil); err != nil {
		return fmt.Errorf("enter isp: %w", err)
	}
	for _, w := range [][2]byte{{regBank, bankSPI}, {7, 0x10}, {1, 3}} {
		if err := d.regWrite(uint16(w[0]), w[1]); err != nil {
			return fmt.Errorf("enter isp: %w", err)
		}
	}
	return nil
}

// exitISP resets the chip. It drops off the bus mid-request, so a protocol
// error here is the normal outcome.
func (d *dev) exitISP() error {
	_, err := d.control(reqOut, cmdISPExit, 0x5835, 0x8280, nil)
	if err != nil && !errors.Is(err, syscall.EPROTO) && !errors.Is(err, syscall.ENODEV) {
		return fmt.Errorf("exit isp: %w", err)
	}
	return nil
}

func (d *dev) spiReady(tries int) error {
	b := make([]byte, 1)
	for i := 0; ; i++ {
		if _, err := d.control(reqIn, cmdSPIReady, 0, 0, b); err != nil {
			return fmt.Errorf("spi ready: %w", err)
		}
		if b[0] != 0 {
			return nil
		}
		if i >= tries {
			return errors.New("spi ready: timeout")
		}
		time.Sleep(time.Millisecond)
	}
}

// spiCommand sends cmd plus data as one SPI transaction with nothing read back.
func (d *dev) spiCommand(cmd byte, data ...byte) error {
	_, err := d.control(reqOut, cmdSPIWrite, 0, 1, append([]byte{cmd}, data...))
	return err
}

// spiQuery sends cmd, then clocks in n bytes, in one SPI transaction.
func (d *dev) spiQuery(cmd []byte, n int) ([]byte, error) {
	if _, err := d.control(reqOut, cmdSPIWriteRead, uint16(n), 1, cmd); err != nil {
		return nil, fmt.Errorf("spi query: %w", err)
	}
	if err := d.spiReady(100); err != nil {
		return nil, err
	}
	out := make([]byte, n)
	got, err := d.control(reqIn, cmdSPIFetch, 0, 0, out)
	if err != nil {
		return nil, fmt.Errorf("spi fetch: %w", err)
	}
	if got != n {
		return nil, fmt.Errorf("spi fetch: short read %d of %d", got, n)
	}
	return out, nil
}

type flashID [3]byte

func (id flashID) size() (int, error) {
	if id[2] < 0x10 || id[2] > 0x18 {
		return 0, fmt.Errorf("jedec id % x: cannot infer flash size", id[:])
	}
	return 1 << id[2], nil
}

// pageSize follows the vendor tool: one flash family takes 32-byte programs.
func (id flashID) pageSize() int {
	if id[0] == 0xc2 && id[1] == 0x22 {
		return 0x20
	}
	return 0x100
}

func (d *dev) flashID() (flashID, error) {
	b, err := d.spiQuery([]byte{spiReadID}, 3)
	if err != nil {
		return flashID{}, err
	}
	id := flashID{b[0], b[1], b[2]}
	if id == (flashID{}) || id == (flashID{0xff, 0xff, 0xff}) {
		return id, fmt.Errorf("jedec id % x: no flash answered", id[:])
	}
	return id, nil
}

func (d *dev) flashStatus() (byte, error) {
	b, err := d.spiQuery([]byte{spiReadStatus}, 1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (d *dev) flashWaitIdle(tries int, pause time.Duration) error {
	for i := 0; i < tries; i++ {
		sr, err := d.flashStatus()
		if err != nil {
			return err
		}
		if sr&statusBusy == 0 {
			return nil
		}
		time.Sleep(pause)
	}
	return errors.New("flash stayed busy")
}

func (d *dev) flashWriteStatus(v byte) error {
	if err := d.spiCommand(spiWriteEnable); err != nil {
		return err
	}
	if err := d.spiCommand(spiWriteStatus, v); err != nil {
		return err
	}
	return d.flashWaitIdle(1000, 10*time.Millisecond)
}

// flashRead reads a range in 64-byte chunks. Like the vendor tool it reads
// each chunk twice and wants both passes to agree.
func (d *dev) flashRead(addr, length int, progress func(done int)) ([]byte, error) {
	img := make([]byte, 0, length)
	for off := 0; off < length; off += readChunk {
		n := min(readChunk, length-off)
		a := addr + off
		cmd := []byte{spiRead, byte(a >> 16), byte(a >> 8), byte(a)}
		var good []byte
		for try := 0; try < 3 && good == nil; try++ {
			x, err := d.spiQuery(cmd, n)
			if err != nil {
				return nil, fmt.Errorf("read %#x: %w", a, err)
			}
			y, err := d.spiQuery(cmd, n)
			if err != nil {
				return nil, fmt.Errorf("read %#x: %w", a, err)
			}
			if bytes.Equal(x, y) {
				good = x
			}
		}
		if good == nil {
			return nil, fmt.Errorf("read %#x: unstable data", a)
		}
		img = append(img, good...)
		if progress != nil {
			progress(off + n)
		}
	}
	return img, nil
}

func (d *dev) flashProgramPage(addr int, page []byte) error {
	if err := d.flashWaitIdle(100, time.Millisecond); err != nil {
		return err
	}
	if err := d.spiCommand(spiWriteEnable); err != nil {
		return err
	}
	head := []byte{spiPageProgram, byte(addr >> 16), byte(addr >> 8), byte(addr)}
	if _, err := d.control(reqOut, cmdSPIWrite, 0, 0, head); err != nil {
		return err
	}
	if _, err := d.control(reqOut, cmdSPIWrite, 0, 1, page); err != nil {
		return err
	}
	time.Sleep(time.Millisecond)
	return d.spiReady(10)
}

type writeOpts struct {
	backup string // where the old contents go before anything is erased
	log    func(format string, a ...any)
}

const burnAttempts = 3

func (d *dev) flashEraseSector(addr int) error {
	if err := d.spiCommand(spiWriteEnable); err != nil {
		return err
	}
	if err := d.spiCommand(spiSectorErase, byte(addr>>16), byte(addr>>8), byte(addr)); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	return d.flashWaitIdle(1000, 10*time.Millisecond)
}

// flashWriteSector erases one 4 KB sector, programs want into it and reads
// it back.
func (d *dev) flashWriteSector(id flashID, addr int, want []byte) error {
	if err := d.flashEraseSector(addr); err != nil {
		return fmt.Errorf("erase %#x: %w", addr, err)
	}
	blank, err := d.flashRead(addr, sectorSize, nil)
	if err != nil {
		return err
	}
	for i, b := range blank {
		if b != 0xff {
			return fmt.Errorf("blank check failed at %#x", addr+i)
		}
	}
	page := id.pageSize()
	for off := 0; off < sectorSize; off += page {
		chunk := want[off : off+page]
		if bytes.Count(chunk, []byte{0xff}) == page {
			continue
		}
		if err := d.flashProgramPage(addr+off, chunk); err != nil {
			return fmt.Errorf("program %#x: %w", addr+off, err)
		}
	}
	if err := d.flashWaitIdle(100, time.Millisecond); err != nil {
		return err
	}
	got, err := d.flashRead(addr, sectorSize, nil)
	if err != nil {
		return err
	}
	for i := range got {
		if got[i] != want[i] {
			return fmt.Errorf("verify failed at %#x: read %02x, want %02x", addr+i, got[i], want[i])
		}
	}
	return nil
}

// flashSync makes the flash equal want, rewriting only the sectors that
// differ. It re-reads the flash on every attempt, so it picks up where a
// failed attempt stopped.
func (d *dev) flashSync(id flashID, want []byte, o writeOpts, what string) error {
	var err error
	for attempt := 1; attempt <= burnAttempts; attempt++ {
		var cur []byte
		if cur, err = d.flashRead(0, len(want), nil); err != nil {
			continue
		}
		err = nil
		for addr := 0; addr < len(want) && err == nil; addr += sectorSize {
			if bytes.Equal(cur[addr:addr+sectorSize], want[addr:addr+sectorSize]) {
				continue
			}
			o.log("%s: sector %#06x, attempt %d", what, addr, attempt)
			err = d.flashWriteSector(id, addr, want[addr:addr+sectorSize])
		}
		if err == nil {
			return nil
		}
		o.log("failed: %v", err)
	}
	return err
}

// flashUnprotect clears the block-protect bits and returns the old status.
func (d *dev) flashUnprotect(o writeOpts) (old byte, err error) {
	if old, err = d.flashStatus(); err != nil || old&statusProtect == 0 {
		return old, err
	}
	o.log("clearing write protection (status %02x)", old)
	if err := d.flashWriteStatus(0); err != nil {
		return old, err
	}
	sr, err := d.flashStatus()
	if err != nil {
		return old, err
	}
	if sr&statusProtect != 0 {
		return old, fmt.Errorf("status register stayed at %02x: flash is hardware write-protected, nothing was erased", sr)
	}
	return old, nil
}

func (d *dev) flashReprotect(old byte, o writeOpts) error {
	if old&statusProtect == 0 {
		return nil
	}
	o.log("restoring write protection")
	return d.flashWriteStatus(old & statusProtect)
}

// flashSelfTest exercises erase, program and read-back on the last sector,
// which must be empty, and leaves it empty. The firmware is not touched.
func (d *dev) flashSelfTest(o writeOpts) error {
	id, err := d.flashID()
	if err != nil {
		return err
	}
	size, err := id.size()
	if err != nil {
		return err
	}
	addr := size - sectorSize
	before, err := d.flashRead(addr, sectorSize, nil)
	if err != nil {
		return err
	}
	if bytes.Count(before, []byte{0xff}) != sectorSize {
		return fmt.Errorf("sector %#x is not empty, refusing to test on it", addr)
	}
	old, err := d.flashUnprotect(o)
	if err != nil {
		return err
	}
	pattern := bytes.Repeat([]byte{0xff}, sectorSize)
	for i := 0; i < 0x100; i++ {
		pattern[i] = byte(i) ^ 0x5a
		pattern[sectorSize-0x100+i] = byte(i*7) ^ 0xa5
	}
	o.log("writing a test pattern to sector %#x", addr)
	testErr := d.flashWriteSector(id, addr, pattern)
	o.log("erasing it again")
	if err := d.flashWriteSector(id, addr, before); err != nil && testErr == nil {
		testErr = fmt.Errorf("clean-up: %w", err)
	}
	if err := d.flashReprotect(old, o); err != nil && testErr == nil {
		testErr = err
	}
	return testErr
}

// flashWrite replaces the flash contents with img: unprotect, rewrite the
// sectors that differ, verify, then put the protection bits back. The caller
// is in ISP mode.
//
// invalid reports that the flash holds neither the new image nor the old one.
// The caller must then leave the chip in ISP mode: a reset would boot whatever
// is there, and a sealed device has no other way back.
func (d *dev) flashWrite(img []byte, o writeOpts) (invalid bool, err error) {
	id, err := d.flashID()
	if err != nil {
		return false, err
	}
	size, err := id.size()
	if err != nil {
		return false, err
	}
	if len(img) > size {
		return false, fmt.Errorf("image is %d bytes, flash holds %d", len(img), size)
	}
	o.log("reading current contents (jedec id % x, %d bytes)", id[:], size)
	old, err := d.flashRead(0, size, nil)
	if err != nil {
		return false, err
	}
	want := bytes.Repeat([]byte{0xff}, size)
	copy(want, img)
	if bytes.Equal(old, want) {
		o.log("flash already holds this image, nothing to do")
		return false, nil
	}
	if err := os.WriteFile(o.backup, old, 0o444); err != nil {
		return false, fmt.Errorf("backup: %w", err)
	}
	o.log("backup written to %s", o.backup)

	oldStatus, err := d.flashUnprotect(o)
	if err != nil {
		return false, err
	}
	burnErr := d.flashSync(id, want, o, "writing")
	if burnErr != nil {
		if err := d.flashSync(id, old, o, "restoring"); err != nil {
			return true, fmt.Errorf("new image: %v; previous firmware: %w", burnErr, err)
		}
		burnErr = fmt.Errorf("new image would not verify (%w); the previous firmware is back in place", burnErr)
	}
	if err := d.flashReprotect(oldStatus, o); err != nil && burnErr == nil {
		burnErr = err
	}
	return false, burnErr
}
