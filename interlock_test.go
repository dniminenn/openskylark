package main

import (
	"errors"
	"strings"
	"testing"
)

// fakeChip models the registers the tool touches and a firmware that takes
// mailbox commands a few polls after they are written. It records any use
// of the I2C master while the firmware was free to use it too.
type fakeChip struct {
	bank       byte
	regs       [16][256]byte
	mask       byte // what the firmware is skipping
	deaf       bool // firmware never takes a command
	nack       bool // nothing on the I2C bus acknowledges
	polls      int
	ram        map[uint16]byte // firmware RAM that host command 0xd0 reads
	late       int             // register reads between taking 0xd0 and each answer byte
	owed       []func()        // answer bytes not written yet
	wait       int
	violations int
	i2cUses    int
}

func (c *fakeChip) transfer(reqType, req uint8, value, index uint16, buf []byte) (int, error) {
	reg := byte(index)
	switch {
	case req == cmdRegWrite && reg == regBank:
		c.bank = buf[0] & 0x0f
	case req == cmdRegRead && reg == regBank:
		buf[0] = c.bank
	case req == cmdRegWrite:
		c.touch(reg)
		c.regs[c.bank][reg] = buf[0]
		if c.bank == bankMain && reg == regMailCommand {
			c.polls = 0
		}
	case req == cmdRegRead:
		c.touch(reg)
		if len(c.owed) > 0 {
			if c.wait--; c.wait <= 0 {
				c.owed[0]()
				c.owed, c.wait = c.owed[1:], c.late
			}
		}
		if c.bank == bankMain && reg == regMailCommand && !c.deaf {
			if cmd := c.regs[0][regMailCommand]; cmd != 0 && cmd&1 == 0 {
				if c.polls++; c.polls >= 3 { // the firmware comes round its loop
					c.take(cmd)
				}
			}
		}
		buf[0] = c.regs[c.bank][reg]
		if c.bank == bankI2C && reg == regI2CCtl {
			buf[0] |= i2cDone
			if c.nack {
				buf[0] |= i2cNoAck
			}
		}
	}
	return len(buf), nil
}

// wake is a firmware that was busy coming round its main loop at last: it
// takes whatever command is waiting.
func (c *fakeChip) wake() {
	if cmd := c.regs[0][regMailCommand]; cmd != 0 && cmd&1 == 0 {
		c.take(cmd)
	}
}

func (c *fakeChip) take(cmd byte) {
	switch cmd {
	case mailLoopMask:
		c.mask = c.regs[0][regMailParam]
	case mailReadRAM:
		at := uint16(c.regs[0][regMailParam])<<8 | uint16(c.regs[0][regMailParam2])
		for i := uint16(0); i < 4; i++ {
			i, v := i, c.ram[at+i]
			write := func() { c.regs[0][regMailAnswer+i] = v }
			if c.late == 0 {
				write()
			} else {
				c.owed = append(c.owed, write)
			}
		}
		c.wait = c.late
	}
	c.regs[0][regMailCommand] = cmd | 1
}

func (c *fakeChip) touch(reg byte) {
	if c.bank != bankI2C || reg < regI2CCtl || reg > regI2CXfer {
		return
	}
	c.i2cUses++
	if c.mask&(maskEvents|maskPower) != maskEvents|maskPower {
		c.violations++
	}
}

func fakeDev(c *fakeChip) *dev { return &dev{transfer: c.transfer} }

// The firmware is paused for exactly the length of each I2C transaction.
func TestI2CPausesFirmware(t *testing.T) {
	c := &fakeChip{}
	d := fakeDev(c)
	if _, err := d.i2cReadReg(0x48, 0x40, 1); err != nil {
		t.Fatal(err)
	}
	if err := d.i2cWriteReg(0x48, 15, []byte{0xff}); err != nil {
		t.Fatal(err)
	}
	if c.i2cUses == 0 || c.violations != 0 {
		t.Errorf("%d I2C register accesses, %d of them with the firmware running", c.i2cUses, c.violations)
	}
	if c.mask != 0 || d.paused || c.bank != bankMain {
		t.Errorf("afterwards: firmware mask %#02x, paused %v, bank %#x", c.mask, d.paused, c.bank)
	}
}

// A firmware that does not take the command is not talked over.
func TestI2CRefusesWhenFirmwareDoesNotAnswer(t *testing.T) {
	c := &fakeChip{deaf: true}
	d := fakeDev(c)
	if _, err := d.i2cReadReg(0x48, 0x40, 1); !errors.Is(err, errFirmwareBusy) {
		t.Errorf("error %v, want the firmware-busy one", err)
	}
	if c.i2cUses != 0 {
		t.Errorf("the I2C master was used %d times anyway", c.i2cUses)
	}
	// The firmware was only busy and comes round after the tool gave up:
	// it must not find a pause waiting for it.
	c.wake()
	if c.mask != 0 || d.paused {
		t.Errorf("a firmware that answers late is left paused (mask %#02x)", c.mask)
	}
	d.noPause = true // a chip with no firmware running: the user says so
	if _, err := d.i2cReadReg(0x48, 0x40, 1); err != nil || c.i2cUses == 0 {
		t.Errorf("with -nopause: error %v, %d I2C accesses", err, c.i2cUses)
	}
}

// An I2C failure still lets the firmware go.
func TestI2CResumesAfterError(t *testing.T) {
	c := &fakeChip{nack: true}
	d := fakeDev(c)
	if _, err := d.i2cReadReg(0x48, 0x40, 1); !errors.Is(err, errNoAck) {
		t.Errorf("error %v, want no acknowledge", err)
	}
	if c.mask != 0 || c.violations != 0 {
		t.Errorf("firmware mask %#02x afterwards, %d unprotected accesses", c.mask, c.violations)
	}
}

// The counters are read with host command 0xd0, and only believed from
// firmware that has put its mark on them.
func TestTelemetryRead(t *testing.T) {
	c := &fakeChip{ram: map[uint16]byte{}}
	d := fakeDev(c)
	c.bank = 7
	got, err := d.telemetry()
	if err != nil || got.present {
		t.Errorf("no block: %+v, %v", got, err)
	}
	for i, v := range []byte{telemetryMagic, 1, 2, 3, 4, 5, 96, 108, 7, 8, 9, 10, 11, 12, 0x15, 0x13} {
		c.ram[telemetryAddr+uint16(i)] = v
	}
	got, err = d.telemetry()
	want := telemetry{true, 2, 3, 4, 5, 96, 108, 7, 8, 9, 10, 11, 12, 0x15, 0x13}
	if err != nil || got != want {
		t.Errorf("got %+v, %v", got, err)
	}
	if c.bank != 7 {
		t.Errorf("bank left at %#x", c.bank)
	}
	if s := got.String(); !strings.Contains(s, "buffer level 192 to 216") || !strings.Contains(s, "5 stream starts") {
		t.Errorf("printed as %q", s)
	}
	// The firmware marks a command taken before the answer is there.
	for c.late = 1; c.late <= 3; c.late++ {
		if got, err = d.telemetry(); err != nil || got != want {
			t.Errorf("answers %d reads late: got %+v, %v", c.late, got, err)
		}
	}
	c.late = 0
	c.ram[telemetryAddr+1] = 2 // a layout this tool does not know
	if got, err = d.telemetry(); err != nil || got.present {
		t.Errorf("unknown version: %+v, %v", got, err)
	}
	c.deaf = true
	if got, err = d.telemetry(); err != nil || got.present {
		t.Errorf("no firmware: %+v, %v", got, err)
	}
}
