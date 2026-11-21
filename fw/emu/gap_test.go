package emu

import "testing"

// A host whose audio underruns stops sending for a few ms and starts again
// without any request. The vendor firmware mutes and starts the stream over,
// which costs more than 100 ms of silence each time. The robust build keeps
// a PCM stream unmuted through the gap and resets the buffer when the data
// is back.
func TestGapRiddenOut(t *testing.T) {
	const ms = 1000 // instructions
	for _, v := range variants(t) {
		im := fixed{v.name, v.path, v.sym["stream"]}
		if !v.robust {
			im.state = 0x27e4
		}
		cpu, chip := start(t, im, 48000, 0xd8, 0)
		run(t, cpu, 1_500_000)
		if chip.RAM[im.state] != 0x15 {
			t.Fatalf("%s: stream state %#02x, want playing", v.name, chip.RAM[im.state])
		}
		muted := func() bool { return chip.Slave[0x07]&0x03 != 0 }
		resets := len(pathResets(chip.Raw))

		chip.Regs[3][0x18] &^= 0x08
		run(t, cpu, 5*ms)
		mutedInGap := muted()
		chip.Regs[3][0x18] |= 0x08
		run(t, cpu, 20*ms)
		mutedAfter := muted()
		t.Logf("%-8s 5 ms gap: muted during it %v, 20 ms after it %v, state %#02x", v.name, mutedInGap, mutedAfter, chip.RAM[im.state])
		if !v.robust {
			if !mutedInGap || !mutedAfter {
				t.Errorf("%s: expected the vendor firmware to mute for a 5 ms gap", v.name)
			}
			continue
		}
		if mutedInGap || mutedAfter || chip.RAM[im.state] != 0x15 {
			t.Errorf("%s: a 5 ms gap muted the output (state %#02x)", v.name, chip.RAM[im.state])
		}
		if chip.Regs[0][0xa6]&4 != 0 || chip.Regs[2][0x09]&2 != 0 {
			t.Errorf("%s: after the gap the status says muted (%#02x) or the mute pin is on (%#02x)", v.name, chip.Regs[0][0xa6], chip.Regs[2][0x09])
		}
		tel := chip.RAM[telemetryAt : telemetryAt+16]
		if n := len(pathResets(chip.Raw)) - resets; n != 0 || chip.Regs[0][0x98] != 1 || chip.Regs[0][0x96] != 0 || tel[5] != 1 || tel[4] != 0 {
			t.Errorf("%s: after the gap: %d buffer resets, gap count %d, reset count %d, starts %d, stops for no data %d",
				v.name, n, chip.Regs[0][0x98], chip.Regs[0][0x96], tel[5], tel[4])
		}

		// A second, shorter one is counted and nothing else.
		chip.Regs[3][0x18] &^= 0x08
		run(t, cpu, 1500)
		chip.Regs[3][0x18] |= 0x08
		run(t, cpu, 20*ms)
		if n := len(pathResets(chip.Raw)) - resets; n != 0 || chip.Regs[0][0x98] != 2 || muted() {
			t.Errorf("%s: after a second gap: %d buffer resets, gap count %d, muted %v", v.name, n, chip.Regs[0][0x98], muted())
		}

		// The host selects alternate setting 0 in the middle of a gap:
		// that is the end of the stream, at once.
		chip.Regs[3][0x18] &^= 0x08
		run(t, cpu, 5*ms)
		chip.Regs[0][0x10] |= 0x04
		chip.Record(0, 0, 1, 0)
		cpu.IRQ0 = true
		run(t, cpu, 30*ms)
		if !muted() {
			t.Errorf("%s: still unmuted 30 ms after the host closed the stream in a gap (state %#02x)", v.name, chip.RAM[im.state])
		}
	}

	// Data that does not come back: the stream is stopped after 100 ms.
	v := robustVariant(t)
	im := fixed{v.name, v.path, v.sym["stream"]}
	cpu, chip := playing(t, im)
	muted := func() bool { return chip.Slave[0x07]&0x03 != 0 }
	chip.Regs[3][0x18] &^= 0x08
	run(t, cpu, 90*ms)
	early := muted()
	run(t, cpu, 40*ms)
	tel := chip.RAM[telemetryAt : telemetryAt+16]
	if early || !muted() || tel[4] != 1 || chip.Regs[0][0x98] != 0 {
		t.Errorf("no data: muted at 90 ms %v, at 130 ms %v, stops for no data %d, gap count %d", early, muted(), tel[4], chip.Regs[0][0x98])
	}
	// And started again when it does.
	chip.Regs[3][0x18] |= 0x08
	run(t, cpu, 1_500_000)
	if muted() || chip.RAM[im.state] != 0x15 || tel[5] != 2 {
		t.Errorf("data back after a stop: muted %v, state %#02x, starts %d", muted(), chip.RAM[im.state], tel[5])
	}

	// A buffer left off centre by a gap is reset at the next look, even if
	// the level watch had just reset it and would be resting for a second.
	cpu, chip = playing(t, im)
	chip.Regs[7][0x1d], chip.Regs[7][0x1e] = 0xe0, 1 // reads full
	run(t, cpu, 100*ms)
	first := len(pathResets(chip.Raw))
	run(t, cpu, 200*ms)
	resting := len(pathResets(chip.Raw)) - first
	chip.Regs[3][0x18] &^= 0x08
	run(t, cpu, 5*ms)
	chip.Regs[3][0x18] |= 0x08
	run(t, cpu, 50*ms)
	afterGap := len(pathResets(chip.Raw)) - first
	if resting != 0 || afterGap != 1 || muted() {
		t.Errorf("off-centre buffer: %d resets while resting, %d within 50 ms of a gap, muted %v", resting, afterGap, muted())
	}

	// The host mutes and unmutes during a gap: both must take.
	cpu, chip = playing(t, im)
	hostMute := func(on byte) {
		chip.RAM[0x21c0] = on
		chip.Regs[0][0x10] |= 0x02
		chip.Record(0, 1, 0, 0x03)
		cpu.IRQ0 = true
		run(t, cpu, 10*ms)
	}
	chip.Regs[3][0x18] &^= 0x08
	run(t, cpu, 5*ms)
	hostMute(1)
	on := muted()
	inGap := chip.RAM[im.state] == 0x16
	hostMute(0)
	chip.Regs[3][0x18] |= 0x08
	run(t, cpu, 1_500_000)
	if !on || !inGap || muted() || chip.RAM[im.state] != 0x15 {
		t.Errorf("host mute in a gap: muted after mute %v, still in the gap %v; after unmute and data: muted %v, state %#02x", on, inGap, muted(), chip.RAM[im.state])
	}
}

// The vendor firmware leaves the DAC at its slowest lock setting; the robust
// build selects the fastest.
func TestDACLockSpeed(t *testing.T) {
	for _, v := range variants(t) {
		_, chip, _ := bootPrompt(t, v.path, 2_000_000)
		want := byte(0x00)
		if v.robust {
			want = 0x0f
		}
		if chip.Slave[10] != want {
			t.Errorf("%s: DAC register 10 is %#02x, want %#02x", v.name, chip.Slave[10], want)
		}
	}
}
