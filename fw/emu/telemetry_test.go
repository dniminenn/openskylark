package emu

import (
	"os"
	"strings"
	"testing"
)

// The telemetry block is at a fixed address so that a host can find it
// without a link map. It must lie above everything the linker placed.
const telemetryAt = 0x27f0

func TestTelemetryClearOfVariables(t *testing.T) {
	data, err := os.ReadFile("../build/robust/robust.map")
	if err != nil {
		t.Skipf("%v (run make test in fw/)", err)
	}
	var start, length, linked uint32
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "_telemetry" {
			linked = hex32(f[0])
		}
		if len(f) >= 3 && f[0] == "C:" {
			switch f[2] {
			case "s_XSEG":
				start = hex32(f[1])
			case "l_XSEG":
				length = hex32(f[1])
			}
		}
	}
	if start == 0 || start+length > telemetryAt {
		t.Errorf("variables occupy %#x to %#x, telemetry starts at %#x", start, start+length, telemetryAt)
	}
	if linked != telemetryAt {
		t.Errorf("telemetry linked at %#x", linked)
	}
}

func hex32(s string) uint32 {
	var v uint32
	for _, c := range s {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint32(c - '0')
		case c >= 'A' && c <= 'F':
			v |= uint32(c-'A') + 10
		case c >= 'a' && c <= 'f':
			v |= uint32(c-'a') + 10
		}
	}
	return v
}

// The level range is published every 48 slow ticks; a change shows alone
// after two of those.
const window = 2_500_000

// What the block says must match what happened.
func TestTelemetry(t *testing.T) {
	im := fixedImages(t)[0]
	playAlt := symbolsOf(t, "../build/robust/robust.map")["play_alt"]
	cpu, chip := playing(t, im)
	tel := func() []byte { return chip.RAM[telemetryAt : telemetryAt+16] }
	const (
		magic = iota
		version
		atStart
		whilePlaying
		dataLost
		starts
		levelMin
		levelMax
		recordsLost
		dacFaults
		busResets
		suspends
		rateChanges
		formatChanges
		state
		rateAlt
	)
	want := func(when string, field int, name string, v byte) {
		t.Helper()
		if tel()[field] != v {
			t.Errorf("%s: %s = %d, want %d (block % x)", when, name, tel()[field], v, tel())
		}
	}

	run(t, cpu, 1_200_000)
	t.Logf("playing: % x", tel())
	want("playing", magic, "magic", 0x54)
	want("playing", version, "version", 1)
	want("playing", atStart, "resets at start", chip.Regs[0][0x95])
	if tel()[atStart] == 0 {
		t.Errorf("no reset at the start of the stream")
	}
	want("playing", whilePlaying, "resets while playing", 0)
	want("playing", dataLost, "data lost", 0)
	want("playing", starts, "starts", 1)
	want("playing", levelMin, "lowest level", 0xd8/2)
	want("playing", levelMax, "highest level", 0xd8/2)
	want("playing", recordsLost, "records lost", 0)
	want("playing", dacFaults, "DAC faults", 0)
	want("playing", suspends, "suspends", 0)
	want("playing", rateChanges, "rate changes", 1)
	want("playing", formatChanges, "format changes", 1)
	want("playing", state, "state", 0x15)
	want("playing", rateAlt, "rate and alternate setting", chip.RAM[playAlt])

	// The host stops sending enough: the buffer runs empty.
	chip.Regs[7][0x1d], chip.Regs[7][0x1e] = 0, 0
	run(t, cpu, 3*second)
	t.Logf("starved: % x", tel())
	want("starved", whilePlaying, "resets while playing", chip.Regs[0][0x96])
	if tel()[whilePlaying] < 3 {
		t.Errorf("starved: %d resets while playing", tel()[whilePlaying])
	}
	want("starved", levelMin, "lowest level", 0)
	want("starved", levelMax, "highest level", 0)
	want("starved", starts, "starts", 1)

	// A level above 255 keeps its top bit.
	chip.Regs[7][0x1d], chip.Regs[7][0x1e] = 0x08, 1
	run(t, cpu, window)
	want("refilled", levelMin, "lowest level", 0x108/2)
	want("refilled", levelMax, "highest level", 0x108/2)

	// The data stops altogether.
	chip.Regs[3][0x18] &^= 0x08
	run(t, cpu, window)
	t.Logf("stopped: % x", tel())
	want("stopped", dataLost, "data lost", 1)
	want("stopped", levelMin, "lowest level", 0xff)
	want("stopped", levelMax, "highest level", 0)
	if tel()[state] == 0x15 {
		t.Errorf("stopped: state still playing")
	}

	// It comes back.
	chip.Regs[7][0x1d], chip.Regs[7][0x1e] = 0xd8, 0
	chip.Regs[3][0x18] |= 0x08
	run(t, cpu, 2*second)
	want("restarted", starts, "starts", 2)
	want("restarted", state, "state", 0x15)

	// A bus reset.
	chip.Regs[0][0x10] |= 0x40
	cpu.IRQ0 = true
	run(t, cpu, 600_000)
	want("bus reset", busResets, "bus resets", 1)

	// The host reads the block with host command 0xd0, four bytes a time.
	for off := 0; off < 16; off += 4 {
		chip.Regs[0][0x91], chip.Regs[0][0x92], chip.Regs[0][0x90] = telemetryAt>>8, byte(telemetryAt+off), 0xd0
		run(t, cpu, 100_000)
		if chip.Regs[0][0x90] != 0xd1 {
			t.Fatalf("read at +%d not taken (%#02x)", off, chip.Regs[0][0x90])
		}
		if got, want := chip.Regs[0][0xa0:0xa4], tel()[off:off+4]; string(got) != string(want) {
			t.Errorf("read at +%d: % x, block has % x", off, got, want)
		}
	}
}
