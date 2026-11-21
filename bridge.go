package main

import (
	"errors"
	"fmt"
	"time"
)

const (
	cmdRegWrite = 0x90 // wIndex = register, 1 data byte
	cmdRegRead  = 0x91

	regBank = 0x00 // selects which register bank the other addresses hit

	bankMain = 0x00
	bankSPI  = 0x0d
	bankI2C  = 0x0e

	// Bank 0 registers kept up to date by the firmware.
	regStreamRate  = 0xa4
	regStreamAux   = 0xa5
	regStreamFlags = 0xa6

	// Bank 0 scratch registers the robust firmware counts in.
	regCentredAtStart = 0x95
	regCentredPlaying = 0x96
	regGaps           = 0x98 // gaps in a playing stream's data that it stayed unmuted through

	// Bank 0: the firmware's command mailbox. A command is written to
	// regMailCommand after its parameter; the firmware sets bit 0 of the
	// command when it takes it, at the top of its main loop.
	regMailCommand = 0x90
	regMailParam   = 0x91
	regMailParam2  = 0x92
	regMailAnswer  = 0xa0 // four bytes
	mailReadRAM    = 0xd0 // parameters: address high, low; answers four bytes of firmware RAM
	mailLoopMask   = 0xd8 // parameter: parts of the main loop to skip
	maskEvents     = 0x01 // requests, the stream state machine: every DAC write
	maskPower      = 0x04 // suspend and resume, which rewrites the DAC

	// Bank 0x0e: I2C master.
	regI2CCtl  = 0x10
	regI2CCfg  = 0x11
	regI2CTx   = 0x12
	regI2CRx   = 0x13
	regI2CXfer = 0x14

	i2cStart   = 0xc0
	i2cStop    = 0xa0
	i2cRxAck   = 0x80
	i2cRxNack  = 0x90
	i2cDone    = 0x01
	i2cNoAck   = 0x08
	i2cDoWrite = 0x01
	i2cDoRead  = 0x03
)

func (d *dev) regRead(addr uint16) (byte, error) {
	b := make([]byte, 1)
	if _, err := d.control(reqIn, cmdRegRead, 0, addr, b); err != nil {
		return 0, fmt.Errorf("read reg %#x: %w", addr, err)
	}
	return b[0], nil
}

func (d *dev) regWrite(addr uint16, v byte) error {
	if _, err := d.control(reqOut, cmdRegWrite, 0, addr, []byte{v}); err != nil {
		return fmt.Errorf("write reg %#x: %w", addr, err)
	}
	return nil
}

// withBank runs fn with the given bank selected and puts the old one back.
func (d *dev) withBank(bank byte, fn func() error) error {
	old, err := d.regRead(regBank)
	if err != nil {
		return err
	}
	if old != bank {
		if err := d.regWrite(regBank, bank); err != nil {
			return err
		}
		defer d.regWrite(regBank, old)
	}
	return fn()
}

type streamStatus struct {
	rate, aux, flags byte
	// Kept by the robust firmware; zero on firmware without them.
	centredAtStart, centredPlaying, gaps byte
}

func (d *dev) streamStatus() (streamStatus, error) {
	var s streamStatus
	err := d.withBank(bankMain, func() (err error) {
		if s.rate, err = d.regRead(regStreamRate); err != nil {
			return
		}
		if s.aux, err = d.regRead(regStreamAux); err != nil {
			return
		}
		if s.flags, err = d.regRead(regStreamFlags); err != nil {
			return
		}
		if s.centredAtStart, err = d.regRead(regCentredAtStart); err != nil {
			return
		}
		if s.centredPlaying, err = d.regRead(regCentredPlaying); err != nil {
			return
		}
		s.gaps, err = d.regRead(regGaps)
		return
	})
	return s, err
}

// The firmware publishes a rate code in 0xa4 and path flags in 0xa6. The
// code is a PCM rate index, or for DoP the DSD rate (0 = DoP64), or for
// native DSD the DSD rate with bit 3 set. Flag bit 1 is set while the output
// stage is in PCM or native DSD mode and clear in DoP mode.
var streamRates = []string{"44.1 kHz", "48 kHz", "88.2 kHz", "96 kHz", "176.4 kHz", "192 kHz", "352.8 kHz", "384 kHz", "705.6 kHz", "768 kHz"}

func (s streamStatus) format() string {
	code := int(s.rate & 0x0f)
	playing := s.flags&4 == 0
	switch {
	case code&8 != 0 && code < 12:
		return fmt.Sprintf("DSD%d", 64<<(code&3))
	case playing && s.flags&2 == 0 && code < 4:
		return fmt.Sprintf("DoP%d", 64<<code)
	case code < len(streamRates):
		return streamRates[code]
	}
	return "rate ?"
}

func (s streamStatus) String() string {
	link := "USB disconnected"
	if s.flags&1 != 0 {
		link = "USB connected"
	}
	state := "playing"
	if s.flags&4 != 0 {
		state = "muted"
	}
	out := fmt.Sprintf("%s, %s, %s  (raw a4=%02x a5=%02x a6=%02x)", link, state, s.format(), s.rate, s.aux, s.flags)
	if s.centredAtStart != 0 || s.centredPlaying != 0 {
		out += fmt.Sprintf("; buffer re-centred %d times at stream start, %d while playing", s.centredAtStart, s.centredPlaying)
	}
	if s.gaps != 0 {
		out += fmt.Sprintf("; %d gaps in the data bridged", s.gaps)
	}
	return out
}

// ramRead returns four bytes of the firmware's RAM, or errFirmwareBusy if the
// firmware does not take the command.
func (d *dev) ramRead(addr uint16) (b [4]byte, err error) {
	err = d.withBank(bankMain, func() error {
		if err := d.regWrite(regMailParam, byte(addr>>8)); err != nil {
			return err
		}
		if err := d.regWrite(regMailParam2, byte(addr)); err != nil {
			return err
		}
		if err := d.regWrite(regMailCommand, mailReadRAM); err != nil {
			return err
		}
		for deadline := time.Now().Add(300 * time.Millisecond); ; {
			v, err := d.regRead(regMailCommand)
			if err != nil {
				return err
			}
			if v == mailReadRAM|1 {
				break
			}
			if time.Now().After(deadline) {
				return errFirmwareBusy
			}
		}
		// The firmware marks a command taken before it writes the answer,
		// so the answer is read until two readings agree.
		var prev [4]byte
		for try := 0; ; try++ {
			for i := range b {
				if b[i], err = d.regRead(regMailAnswer + uint16(i)); err != nil {
					return err
				}
			}
			if try > 0 && b == prev {
				return nil
			}
			if try == 8 {
				return errors.New("the answer to a firmware RAM read never settled")
			}
			prev = b
		}
	})
	return
}

// The robust firmware keeps counters of what it has seen in a block at a
// fixed RAM address (fw/src/ct7601.h). They wrap at 256 and start again
// from zero when the DAC loses power.
const (
	telemetryAddr  = 0x27f0
	telemetryMagic = 0x54
)

type telemetry struct {
	present                                 bool
	atStart, playing, dataLost, starts      byte
	levelMin, levelMax                      byte // halved; 0xff and 0 when nothing played
	recordsLost, dacFaults                  byte
	busResets, suspends                     byte
	rateChanges, formatChanges, state, rate byte
}

// telemetry reads the block. Firmware without one, or none running, gives
// a zero telemetry and no error.
func (d *dev) telemetry() (telemetry, error) {
	var raw [16]byte
	for off := 0; off < len(raw); off += 4 {
		b, err := d.ramRead(telemetryAddr + uint16(off))
		if errors.Is(err, errFirmwareBusy) {
			return telemetry{}, nil
		}
		if err != nil {
			return telemetry{}, err
		}
		if off == 0 && (b[0] != telemetryMagic || b[1] != 1) {
			return telemetry{}, nil
		}
		copy(raw[off:], b[:])
	}
	return telemetry{true, raw[2], raw[3], raw[4], raw[5], raw[6], raw[7], raw[8], raw[9], raw[10], raw[11], raw[12], raw[13], raw[14], raw[15]}, nil
}

func (t telemetry) String() string {
	level := "nothing played in the last second"
	if t.levelMin <= t.levelMax {
		level = fmt.Sprintf("buffer level %d to %d in the last second", int(t.levelMin)*2, int(t.levelMax)*2)
	}
	return fmt.Sprintf("%d stream starts, %d buffer resets at a start, %d while playing, %d times the data stopped, "+
		"%d lost requests, %d DAC faults, %d bus resets, %d suspends, %d rate requests, %d format changes; %s; stream state %#02x",
		t.starts, t.atStart, t.playing, t.dataLost, t.recordsLost, t.dacFaults, t.busResets, t.suspends,
		t.rateChanges, t.formatChanges, level, t.state)
}

// The I2C helpers below must run inside withBank(bankI2C, ...).

func (d *dev) i2cWait() error {
	for i := 0; i < 20; i++ {
		v, err := d.regRead(regI2CCtl)
		if err != nil {
			return err
		}
		if v&i2cDone != 0 {
			return nil
		}
	}
	return errors.New("i2c: master did not finish")
}

func (d *dev) i2cSend(b byte) (acked bool, err error) {
	if err = d.regWrite(regI2CXfer, i2cDoWrite); err != nil {
		return
	}
	if err = d.regWrite(regI2CTx, b); err != nil {
		return
	}
	if err = d.i2cWait(); err != nil {
		return
	}
	v, err := d.regRead(regI2CCtl)
	return v&i2cNoAck == 0, err
}

func (d *dev) i2cRecv(last bool) (byte, error) {
	ctl := byte(i2cRxAck)
	if last {
		ctl = i2cRxNack
	}
	if err := d.regWrite(regI2CCtl, ctl); err != nil {
		return 0, err
	}
	if err := d.regWrite(regI2CXfer, i2cDoRead); err != nil {
		return 0, err
	}
	if err := d.i2cWait(); err != nil {
		return 0, err
	}
	return d.regRead(regI2CRx)
}

// i2cAddress sends START plus the address byte, retrying like the vendor tool.
func (d *dev) i2cAddress(addr8 byte, tries int) error {
	for i := 0; i < tries; i++ {
		if err := d.regWrite(regI2CCtl, i2cStart); err != nil {
			return err
		}
		ok, err := d.i2cSend(addr8)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errNoAck
}

var errNoAck = errors.New("i2c: no acknowledge")

var errFirmwareBusy = errors.New("the firmware did not take the pause command within 1.5 s (starting up, or not running); try again, or use -nopause if no firmware is running")

// mailLoopMaskSet tells the firmware which parts of its main loop to skip
// and waits until it has taken the command.
func (d *dev) mailLoopMaskSet(mask byte) error {
	return d.withBank(bankMain, func() error {
		if err := d.regWrite(regMailParam, mask); err != nil {
			return err
		}
		if err := d.regWrite(regMailCommand, mailLoopMask); err != nil {
			return err
		}
		for deadline := time.Now().Add(1500 * time.Millisecond); ; {
			v, err := d.regRead(regMailCommand)
			if err != nil {
				return err
			}
			if v == mailLoopMask|1 {
				return nil
			}
			if time.Now().After(deadline) {
				return errFirmwareBusy
			}
		}
	})
}

// mailLoopMaskClear puts "skip nothing" in the mailbox without waiting for
// the firmware to take it. It replaces a pause the firmware has not taken
// yet, so that one taken late pauses nothing, and lifts one it has.
func (d *dev) mailLoopMaskClear() error {
	return d.withBank(bankMain, func() error {
		if err := d.regWrite(regMailParam, 0); err != nil {
			return err
		}
		return d.regWrite(regMailCommand, mailLoopMask)
	})
}

// pauseFirmware keeps the firmware off the I2C bus. The bridge has one I2C
// master and the firmware drives the DAC through it whenever a stream
// starts or stops or the host changes volume; a host transaction in the
// middle of one of those corrupts both. The firmware takes the command
// between passes of its main loop, so once it has, no DAC transaction of
// its own is in progress and none starts until resumeFirmware.
func (d *dev) pauseFirmware() error {
	if d.noPause || d.paused {
		return nil
	}
	// Marked before the command goes out, so that an interrupt while
	// waiting for the firmware still lifts it.
	d.paused = true
	if err := d.mailLoopMaskSet(maskEvents | maskPower); err != nil {
		d.paused = false
		d.mailLoopMaskClear()
		return err
	}
	return nil
}

func (d *dev) resumeFirmware() error {
	if !d.paused {
		return nil
	}
	d.paused = false
	if err := d.mailLoopMaskSet(0); err != nil {
		d.mailLoopMaskClear()
		return err
	}
	return nil
}

func (d *dev) i2c(fn func() error) (err error) {
	if err := d.pauseFirmware(); err != nil {
		return err
	}
	defer func() {
		if e := d.resumeFirmware(); err == nil {
			err = e
		}
	}()
	return d.withBank(bankI2C, func() error {
		if err := d.regWrite(regI2CCfg, 4); err != nil {
			return err
		}
		err := fn()
		if e := d.regWrite(regI2CCtl, i2cStop); err == nil {
			err = e
		}
		return err
	})
}

// i2cProbe reports whether a device acknowledges the 7-bit address.
func (d *dev) i2cProbe(addr7 byte) (bool, error) {
	err := d.i2c(func() error { return d.i2cAddress(addr7<<1, 1) })
	if errors.Is(err, errNoAck) {
		return false, nil
	}
	return err == nil, err
}

func (d *dev) i2cReadReg(addr7, reg byte, n int) ([]byte, error) {
	out := make([]byte, n)
	err := d.i2c(func() error {
		if err := d.i2cAddress(addr7<<1, 10); err != nil {
			return err
		}
		if _, err := d.i2cSend(reg); err != nil {
			return err
		}
		if err := d.i2cAddress(addr7<<1|1, 10); err != nil {
			return err
		}
		for i := range out {
			b, err := d.i2cRecv(i == n-1)
			if err != nil {
				return err
			}
			out[i] = b
		}
		return nil
	})
	return out, err
}

func (d *dev) i2cWriteReg(addr7, reg byte, data []byte) error {
	return d.i2c(func() error {
		if err := d.i2cAddress(addr7<<1, 10); err != nil {
			return err
		}
		for _, b := range append([]byte{reg}, data...) {
			ok, err := d.i2cSend(b)
			if err != nil {
				return err
			}
			if !ok {
				return errNoAck
			}
		}
		return nil
	})
}
