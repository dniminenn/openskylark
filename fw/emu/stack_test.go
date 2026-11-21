package emu

import "testing"

// The vendor firmware keeps everything in the low 128 bytes of internal RAM
// (it clears 0x00-0x7f and nothing higher), so that is all that is known to
// exist. The robust build's stack starts at 0x24 and must stay inside it,
// with interrupts arriving at every depth, and it must never use MOVX @Ri,
// which depends on port 2 paging the vendor code never relies on.
func TestRobustStackAndPaging(t *testing.T) {
	cpu, chip := load(t, "../build/robust/robust.bin")
	maxSP := byte(0)
	// Start-up against a host doing what the Linux probe does: bus reset
	// and high speed 100 to 150 ms after attach, then requests 40 us apart
	// while the firmware may still be inside its attach code.
	attached := -1
	for i := 0; i < 2_000_000; i++ {
		if err := cpu.Step(); err != nil {
			t.Fatalf("after %d instructions: %v", i, err)
		}
		if sp := cpu.SFR[sfrSP-0x80]; sp > maxSP {
			maxSP = sp
		}
		if attached < 0 && chip.Regs[0][0x30]&0x02 != 0 {
			attached = i
		}
		if attached < 0 {
			continue
		}
		switch since := i - attached; {
		case since == 100_000:
			chip.Regs[0][0x10] |= 0x40
			cpu.IRQ0 = true
		case since == 150_000:
			chip.Regs[0][0x32] |= 0x10
		case since == 160_000:
			copy(chip.RAM[0x2000:], []byte{0x00, 0xdc, 0x05, 0x00})
			chip.Regs[0][0x10] |= 0x02
			chip.Record(0, 1, 0, 0x05)
			cpu.IRQ0 = true
		case since > 160_000 && since < 163_000 && since%600 == 0:
			chip.Regs[0][0x10] |= 0x04
			chip.Record(byte(since/600%5), 0, 1, 0)
			cpu.IRQ0 = true
		case since > 165_000 && since < 166_000 && since%40 == 0:
			chip.RAM[0x21c5] = byte(since)
			chip.Regs[0][0x10] |= 0x02
			chip.Record(1, 2, 0, 0x03)
			cpu.IRQ0 = true
		}
	}
	if chip.Regs[0][0xad] == 0 {
		t.Fatal("not in the main loop after start-up")
	}
	step := func(n int) {
		for i := 0; i < n; i++ {
			if err := cpu.Step(); err != nil {
				t.Fatal(err)
			}
			if sp := cpu.SFR[sfrSP-0x80]; sp > maxSP {
				maxSP = sp
			}
			if i%977 == 0 { // a request lands, wherever the firmware happens to be
				chip.Regs[0][0x10] |= 0x02
				chip.Record(1, 2, 0, 0x03)
				cpu.IRQ0 = true
			}
		}
	}
	chip.Regs[7][0x1d] = 0xd8
	for _, hz := range []uint32{48000, 384000, 176400} {
		copy(chip.RAM[0x2000:], []byte{byte(hz), byte(hz >> 8), byte(hz >> 16), byte(hz >> 24)})
		chip.Regs[0][0x10] |= 0x02
		chip.Record(0, 1, 0, 0x05)
		cpu.IRQ0 = true
		step(300_000)
		chip.Regs[0][0x10] |= 0x04
		chip.Record(3, 0, 1, 0)
		cpu.IRQ0 = true
		step(300_000)
		chip.Regs[3][0x18] |= 0x08
		chip.Unlocked = hz == 384000 // the long way through the unmute
		step(1_500_000)
		chip.Regs[7][0x1d] = 0x00 // pinned: the level watch resets the path
		step(100_000)
		chip.Regs[7][0x1d] = 0xd8
		chip.Regs[3][0x18] &^= 0x08
		step(800_000)
		chip.Regs[0][0x10] |= 0x40 // bus reset
		cpu.IRQ0 = true
		step(300_000)
	}
	// Suspend and resume.
	chip.Regs[0][0x10] |= 0x08
	cpu.IRQ0 = true
	step(200_000)
	chip.Regs[0][0x33] |= 0x01
	chip.Regs[0][0x11] |= 0x20
	cpu.IRQ0 = true
	step(200_000)
	chip.Regs[0][0x32] = chip.Regs[0][0x32]&0xf0 | 0x02
	step(600_000)
	t.Logf("stack reached %#02x (starts at 0x24); MOVX @Ri executed %d times; DAC faults %d", maxSP, cpu.Paged, chip.RAM[symbolsOf(t, "../build/robust/robust.map")["dac_faults"]])
	if maxSP >= 0x70 {
		t.Errorf("stack reached %#02x: too close to the top of the 128 bytes known to exist", maxSP)
	}
	if cpu.Paged != 0 {
		t.Errorf("MOVX @Ri executed %d times", cpu.Paged)
	}
	if chip.Regs[0][0xad] == 0 {
		t.Error("main loop not running")
	}
}
