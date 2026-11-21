package emu

import (
	"strconv"
	"strings"
	"testing"
)

// streaming boots an image against a prompt host and plays 48 kHz 32-bit
// until the output is open.
func streaming(t *testing.T, v variant) (*CPU, *Chip) {
	t.Helper()
	cpu, chip, _ := bootPrompt(t, v.path, 2_000_000)
	chip.Regs[7][0x1d] = 0xd8 // a healthy buffer level
	copy(chip.RAM[0x2000:], []byte{0x80, 0xbb, 0x00, 0x00})
	for _, r := range [][5]byte{{0x02, 0, 1, 0, 0x05}, {0x04, 3, 0, 1, 0}} {
		chip.Regs[0][0x10] |= r[0]
		chip.Record(r[1], r[2], r[3], r[4])
		cpu.IRQ0 = true
		run(t, cpu, 600_000)
	}
	chip.Regs[3][0x18] |= 0x08
	run(t, cpu, 1_500_000)
	if chip.Slave[7]&3 != 0 {
		t.Fatalf("%s: not unmuted while playing", v.name)
	}
	return cpu, chip
}

// What the host asked for is applied even when the firmware never saw the
// request: the cause was raised once for two requests, or its record was
// replaced before the handler read it.
func TestLostRequestsRecovered(t *testing.T) {
	for _, v := range variants(t) {
		cpu, chip := streaming(t, v)
		// Host mute and a volume change arrive together; the firmware
		// is told of one request and the record is the volume's.
		chip.RAM[0x21c0] = 1
		copy(chip.RAM[0x21c4:], []byte{0x00, 0xf6})
		chip.Regs[0][0x10] |= 0x02
		chip.Record(0, 2, 0, 0x03)
		cpu.IRQ0 = true
		run(t, cpu, 150_000)
		muted := chip.Slave[7]&3 == 3
		copy(chip.RAM[0x2000:], []byte{0x00, 0x77, 0x01, 0x00}) // 96 kHz, no interrupt
		run(t, cpu, 400_000)
		rate := chip.RAM[v.sym["play_alt"]] >> 4
		t.Logf("%-8s mute applied %v, rate index %d", v.name, muted, rate)
		if v.robust && (!muted || rate != 3) {
			t.Error("robust build did not pick up the settings")
		}
		if !v.robust && (muted || rate == 3) {
			t.Error("expected the vendor image to miss them; the test is not exercising anything")
		}
	}
}

// A cause with no interrupt edge (the edge fell while the latch was being
// cleared) is still serviced.
func TestCauseWithoutEdge(t *testing.T) {
	for _, v := range variants(t) {
		cpu, chip, _ := bootPrompt(t, v.path, 2_000_000)
		chip.Regs[0][0x10] |= 0x04
		chip.Record(3, 0, 1, 0)
		run(t, cpu, 300_000)
		alt := chip.RAM[v.sym["play_alt"]] & 0x0f
		t.Logf("%-8s alternate setting applied: %d, status left pending: %02x", v.name, alt, chip.Regs[0][0x10])
		if v.robust && (alt != 3 || chip.Regs[0][0x10] != 0) {
			t.Error("robust build left the cause unserviced")
		}
		if !v.robust && alt == 3 {
			t.Error("expected the vendor image to stay deaf; the test is not exercising anything")
		}
	}
}

// mutePinReleased returns the position in the write log, at or after from,
// of the first GPIO write with the mute pin low.
func mutePinReleased(raw []string, from int) int {
	for i := from; i < len(raw); i++ {
		if strings.HasPrefix(raw[i], "02:09=") {
			if v, _ := strconv.ParseUint(raw[i][6:], 16, 8); v&0x02 == 0 {
				return i
			}
		}
	}
	return -1
}

// A format change that arrives while the firmware is waiting to unmute must
// not have the output opened on the old format first.
func TestNoUnmuteAcrossFormatChange(t *testing.T) {
	for _, v := range variants(t) {
		cpu, chip, _ := bootPrompt(t, v.path, 2_000_000)
		chip.Regs[7][0x1d] = 0xd8
		copy(chip.RAM[0x2000:], []byte{0x80, 0xbb, 0x00, 0x00})
		for _, r := range [][5]byte{{0x02, 0, 1, 0, 0x05}, {0x04, 3, 0, 1, 0}} {
			chip.Regs[0][0x10] |= r[0]
			chip.Record(r[1], r[2], r[3], r[4])
			cpu.IRQ0 = true
			run(t, cpu, 600_000)
		}
		chip.Regs[3][0x18] |= 0x08
		run(t, cpu, 50_000) // inside the 100 ms wait before unmuting
		mark := len(chip.Raw)
		chip.Regs[0][0x10] |= 0x04
		chip.Record(1, 0, 1, 0) // 16-bit
		cpu.IRQ0 = true
		run(t, cpu, 1_500_000)
		released := mutePinReleased(chip.Raw, mark)
		reframed := -1
		for i := mark; i < len(chip.Raw); i++ {
			if strings.HasPrefix(chip.Raw[i], "07:07=") {
				reframed = i
				break
			}
		}
		early := released >= 0 && reframed >= 0 && released < reframed
		t.Logf("%-8s output opened before the new format was applied: %v", v.name, early)
		if released < 0 || reframed < 0 || chip.Slave[7]&3 != 0 {
			t.Fatalf("%s: mute released at %d, path reframed at %d, DAC register 7 %#02x", v.name, released, reframed, chip.Slave[7])
		}
		if v.robust && early {
			t.Error("robust build opened the output on the old format")
		}
		if !v.robust && !early {
			t.Error("expected the vendor image to open early; the test is not exercising anything")
		}
	}
}

// The output is held until the DAC reports lock, but not for ever.
func TestUnmuteWaitsForLock(t *testing.T) {
	v := robustVariant(t)
	took := func(unlocked bool) int {
		cpu, chip, _ := bootPrompt(t, v.path, 2_000_000)
		chip.Unlocked = unlocked
		chip.Regs[7][0x1d] = 0xd8
		copy(chip.RAM[0x2000:], []byte{0x80, 0xbb, 0x00, 0x00})
		for _, r := range [][5]byte{{0x02, 0, 1, 0, 0x05}, {0x04, 3, 0, 1, 0}} {
			chip.Regs[0][0x10] |= r[0]
			chip.Record(r[1], r[2], r[3], r[4])
			cpu.IRQ0 = true
			run(t, cpu, 600_000)
		}
		chip.Regs[3][0x18] |= 0x08
		mark := len(chip.Raw)
		for i := 0; i < 1_500_000; i++ {
			if err := cpu.Step(); err != nil {
				t.Fatal(err)
			}
			if i%1000 == 0 && mutePinReleased(chip.Raw, mark) >= 0 {
				return i / 1000
			}
		}
		return -1
	}
	locked, unlocked := took(false), took(true)
	t.Logf("unmuted %d ms after data with the DAC locked, %d ms with it never locking", locked, unlocked)
	if locked < 0 || unlocked < 0 || unlocked-locked < 250 || unlocked-locked > 400 {
		t.Errorf("want about 300 ms more when the DAC does not lock")
	}
}

// With nothing answering on the I2C bus, start-up must not take longer than
// the vendor firmware does.
func TestDeadI2CBusAtStartup(t *testing.T) {
	for _, v := range variants(t) {
		m := newMachine(t, v.path, v.sym, !v.robust)
		m.chip.Stuck = true
		m.call(t, "dac_init")
		bytes := 0
		for _, w := range m.chip.Raw {
			if strings.HasPrefix(w, "0e:12=") {
				bytes++
			}
		}
		t.Logf("%-8s tried %d bytes on a bus that never finishes one", v.name, bytes)
		if v.robust && bytes > 3 {
			t.Errorf("robust build tried %d bytes", bytes)
		}
		if !v.robust && bytes < 60 {
			t.Errorf("expected the vendor image to try them all, saw %d", bytes)
		}
	}
}

func init() { originalSymbols["path_state"] = 0x277a }

// Coming back from suspend, the attach mode is chosen by what the device
// can do, not by the speed the last session happened to get, and the bus is
// marked active again without waiting for a bus reset.
func TestResumeKeepsHighSpeed(t *testing.T) {
	for _, v := range variants(t) {
		m := newMachine(t, v.path, v.sym, !v.robust)
		m.set(t, "cfg", 0x40) // the last bus reset found full speed
		if v.robust {
			m.set(t, "usb_high_speed_capable", 0x02)
		}
		m.set(t, "power_state", 11)
		m.call(t, "power_service")
		mode := m.chip.Regs[0][0x30] & 3
		active := m.chip.Regs[2][0x09]&0x04 != 0 && m.chip.RAM[m.addr(t, "path_state")]&0x01 != 0
		t.Logf("%-8s attach mode after resume: %d, bus marked active: %v", v.name, mode, active)
		if v.robust && !active {
			t.Error("robust build left the bus marked inactive after resume")
		}
		if v.robust && mode != 3 {
			t.Error("robust build came back at full speed")
		}
		if !v.robust && mode == 3 {
			t.Error("expected the vendor image to choose full speed; the test is not exercising anything")
		}
	}
}

// Muting and unmuting keep filter bits the host set in the same register,
// and a read that makes no sense does not get written back.
func TestMuteKeepsFilterSettings(t *testing.T) {
	v := robustVariant(t)
	m := newMachine(t, v.path, v.sym, false)
	m.call(t, "dac_init")
	m.chip.Slave[7] = 0xa7 // muted, with filter shape and IIR bandwidth changed
	m.set(t, "stream_format", 0x11)
	m.chip.Regs[7][0x1d] = 0xd8
	m.call(t, "dac_mute", 0)
	if m.chip.Slave[7] != 0xa4 {
		t.Errorf("after unmute: register 7 = %#02x, want 0xa4", m.chip.Slave[7])
	}
	m.chip.Slave[7] = 0xff // as a failed read would look
	m.call(t, "dac_mute", 1)
	if m.chip.Slave[7] != 0xa7 {
		t.Errorf("after a nonsense read: register 7 = %#02x, want 0xa7 from the last write", m.chip.Slave[7])
	}
}

// Debug output must not be able to stop the firmware: on a real CT7601 the
// serial port's transmit-done flag did not come up with the receiver and the
// serial interrupt off, and the first character hung the main loop.
func TestSerialCannotBlock(t *testing.T) {
	v := robustVariant(t)
	cpu, chip, _ := bootPrompt(t, v.path, 2_000_000)
	if cpu.SFR[sfrSCON-0x80]&0xf0 != 0x50 || cpu.SFR[sfrIE-0x80]&0x10 == 0 {
		t.Errorf("serial set-up differs from the vendor's: SCON %#02x, IE %#02x", cpu.SFR[sfrSCON-0x80], cpu.SFR[sfrIE-0x80])
	}
	cpu.NoTI = true
	chip.Regs[7][0x1d] = 0xd8
	copy(chip.RAM[0x2000:], []byte{0x80, 0xbb, 0x00, 0x00})
	for _, r := range [][5]byte{{0x02, 0, 1, 0, 0x05}, {0x04, 3, 0, 1, 0}} {
		chip.Regs[0][0x10] |= r[0]
		chip.Record(r[1], r[2], r[3], r[4])
		cpu.IRQ0 = true
		run(t, cpu, 600_000)
	}
	chip.Regs[3][0x18] |= 0x08
	beat := chip.Regs[0][0xad]
	run(t, cpu, 1_500_000)
	if chip.Slave[7]&3 != 0 || chip.Regs[0][0xad] == beat {
		t.Errorf("with a serial port that never finishes: DAC register 7 %#02x, heartbeat %#02x -> %#02x", chip.Slave[7], beat, chip.Regs[0][0xad])
	}
}

// A host tool pauses the firmware's event handling while it uses the I2C
// bus. If the tool never lets go, the robust build carries on after 2 s; the
// vendor image stays paused until power is removed.
func TestHostPauseExpires(t *testing.T) {
	for _, v := range variants(t) {
		cpu, chip, _ := bootPrompt(t, v.path, 2_000_000)
		chip.Regs[0][0x91], chip.Regs[0][0x90] = 0x05, 0xd8 // mask events and power handling
		run(t, cpu, 100_000)
		if chip.Regs[0][0x90] != 0xd9 {
			t.Fatalf("%s: command not taken (%#02x)", v.name, chip.Regs[0][0x90])
		}
		copy(chip.RAM[0x2000:], []byte{0x80, 0xbb, 0x00, 0x00})
		chip.Regs[0][0x10] |= 0x02
		chip.Record(0, 1, 0, 0x05)
		cpu.IRQ0 = true
		run(t, cpu, 500_000)
		held := chip.RAM[v.sym["play_alt"]]>>4 != 1
		run(t, cpu, 2_500_000)
		applied := chip.RAM[v.sym["play_alt"]]>>4 == 1
		t.Logf("%-8s request held while paused: %v; handled once the pause was 3 s old: %v", v.name, held, applied)
		if !held {
			t.Errorf("%s: the pause did not hold the request back", v.name)
		}
		if v.robust != applied {
			t.Errorf("%s: handled after the pause aged out = %v", v.name, applied)
		}
	}
}

// The link reaches high speed only after the firmware has handled the bus
// reset. The vendor image stays on the full-speed clock setting for good;
// the robust build looks again.
func TestSpeedSettlesAfterReset(t *testing.T) {
	for _, v := range variants(t) {
		cpu, chip, _ := boot(t, v.path, 8_000_000) // no host answering: past all the attach retries
		chip.Regs[0][0x10] |= 0x40                 // bus reset, link not yet at high speed
		cpu.IRQ0 = true
		run(t, cpu, 600_000)
		during := chip.Regs[7][0x40] & 0x0f
		chip.Regs[0][0x32] |= 0x10 // negotiation completes
		run(t, cpu, 1_500_000)
		after := chip.Regs[7][0x40] & 0x0f
		t.Logf("%-8s clock setting %d after the reset, %d once the link is at high speed", v.name, during, after)
		if during != 3 {
			t.Errorf("%s: expected the full-speed setting (3) right after the reset, got %d", v.name, during)
		}
		if v.robust && after != 6 {
			t.Errorf("robust build still on the full-speed clock setting (%d)", after)
		}
		if !v.robust && after != 3 {
			t.Error("expected the vendor image to stay wrong; the test is not exercising anything")
		}
	}
}

// The robust build tells the DAC which input format is coming at each
// stream start (ES9018K2M register 1: 0x80 I2S, 0x83 DSD); the vendor image
// leaves it on auto-detect (0x8c).
func TestDACToldItsInput(t *testing.T) {
	for _, v := range variants(t) {
		cpu, chip, _ := bootPrompt(t, v.path, 2_000_000)
		chip.Regs[7][0x1d] = 0xd8
		req := func(s0, valueLow, valueHigh, indexLow, entity byte) {
			chip.Regs[0][0x10] |= s0
			chip.Record(valueLow, valueHigh, indexLow, entity)
			cpu.IRQ0 = true
			run(t, cpu, 600_000)
		}
		copy(chip.RAM[0x2000:], []byte{0x80, 0xbb, 0x00, 0x00}) // 48 kHz
		req(0x02, 0, 1, 0, 0x05)
		req(0x04, 3, 0, 1, 0)
		chip.Regs[3][0x18] |= 0x08
		run(t, cpu, 1_500_000)
		pcm := chip.Slave[1]
		chip.Regs[3][0x18] &^= 0x08
		run(t, cpu, 1_500_000)
		copy(chip.RAM[0x2000:], []byte{0x88, 0x58, 0x01, 0x00}) // 88.2 kHz frames: DSD64
		req(0x02, 0, 1, 0, 0x05)
		req(0x04, 4, 0, 1, 0)
		chip.Regs[3][0x18] |= 0x08
		run(t, cpu, 2_500_000)
		dsd := chip.Slave[1]
		t.Logf("%-8s DAC register 1: %#02x for PCM, %#02x for DSD", v.name, pcm, dsd)
		if v.robust && (pcm != 0x80 || dsd != 0x83) {
			t.Error("robust build did not select the input")
		}
		if !v.robust && (pcm != 0x8c || dsd != 0x8c) {
			t.Error("expected the vendor image to leave auto-detect on")
		}
	}
}
