package emu

import (
	"strconv"
	"strings"
	"testing"
)

// The emulator's timer counts instructions, 500 to a firmware tick of
// 0.5 ms, so a million instructions stand for a second.
const second = 1_000_000

func load(t *testing.T, path string) (*CPU, *Chip) {
	t.Helper()
	img := readImage(t, path)
	chip := &Chip{Addr: 0x90}
	cpu := &CPU{Bus: chip}
	chip.CPU = cpu
	copy(cpu.Code[:], img)
	return cpu, chip
}

// A host that takes its time to answer must not cost the session its
// transceiver setting, and one that is merely a little slow must not be
// detached from at all.
func TestTrimAfterSlowHost(t *testing.T) {
	const never = -1
	for _, c := range []struct {
		name          string
		answer        int // instructions from attach to high speed
		trimAtReset   byte
		stockTrim     byte
		stockDetaches int
		fixedDetaches int
	}{
		{"host answers in 150 ms", 150_000, 0x10, 0, 0, 0},
		{"host answers after 1 s", second, 0x10, 1, 1, 0},
		{"host answers after 2.5 s", 2_500_000, 0x10, 3, 3, 0},
		{"no host", never, 0x10, 3, 3, 3},
		{"trim left over from an earlier run", 150_000, 0x70, 3, 0, 0},
	} {
		type image struct {
			name, path string
			fixed      bool
		}
		images := []image{{"robust", "../build/robust/robust.bin", true}}
		if originalImage() != nil {
			images = append([]image{{"stock", origPath, false}}, images...)
		}
		for _, im := range images {
			cpu, chip := load(t, im.path)
			chip.Regs[0x0f][0x23] = c.trimAtReset
			attachedAt, detaches, wasAttached := -1, 0, false
			for i := 0; i < 9*second; i++ {
				if err := cpu.Step(); err != nil {
					t.Fatal(err)
				}
				attached := chip.Regs[0][0x30]&0x02 != 0
				if attached && attachedAt < 0 {
					attachedAt = i
				}
				if wasAttached && !attached {
					detaches++
				}
				wasAttached = attached
				if c.answer != never && attachedAt >= 0 && i == attachedAt+c.answer {
					chip.Regs[0][0x32] |= 0x10
				}
			}
			trim := chip.Regs[0x0f][0x23] >> 5 & 3
			wantTrim, wantDetaches := c.stockTrim, c.stockDetaches
			if im.fixed {
				wantTrim, wantDetaches = 0, c.fixedDetaches
			}
			t.Logf("%-36s %-8s trim %d, detached %d times", c.name, im.name, trim, detaches)
			if trim != wantTrim || detaches != wantDetaches {
				t.Errorf("%s, %s: trim %d and %d detaches, want %d and %d", c.name, im.name, trim, detaches, wantTrim, wantDetaches)
			}
			if !wasAttached {
				t.Errorf("%s, %s: left detached", c.name, im.name)
			}
			if chip.Regs[0][0xad] == 0 {
				t.Errorf("%s, %s: the main loop never ran", c.name, im.name)
			}
		}
	}
}

// fixed is an image with the fixes in, and where it keeps the playback
// stream's state.
type fixed struct {
	name, path string
	state      uint16
}

func fixedImages(t *testing.T) []fixed {
	return []fixed{
		{"robust", "../build/robust/robust.bin", symbolsOf(t, "../build/robust/robust.map")["stream"]},
	}
}

// start boots an image against a prompt host, has the host set a sampling
// frequency and select the 32-bit alternate setting, and lets data arrive
// with the buffer level reading as given.
func start(t *testing.T, im fixed, hz uint32, levelLow, levelBit byte) (*CPU, *Chip) {
	t.Helper()
	cpu, chip := load(t, im.path)
	chip.Regs[0][0x32] |= 0x10
	run(t, cpu, 2*second)
	req := func(s0, valueLow, valueHigh, indexLow, entity byte) {
		chip.Regs[0][0x10] |= s0
		chip.Record(valueLow, valueHigh, indexLow, entity)
		cpu.IRQ0 = true
		run(t, cpu, 600_000)
	}
	copy(chip.RAM[0x2000:], []byte{byte(hz), byte(hz >> 8), byte(hz >> 16), byte(hz >> 24)})
	req(0x02, 0, 1, 0, 0x05)
	req(0x04, 3, 0, 1, 0)
	chip.Regs[7][0x1d], chip.Regs[7][0x1e] = levelLow, levelBit
	chip.Regs[3][0x18] |= 0x08
	return cpu, chip
}

// playing is start with a 48 kHz stream and a healthy level, run until the
// output is unmuted.
func playing(t *testing.T, im fixed) (*CPU, *Chip) {
	t.Helper()
	cpu, chip := start(t, im, 48000, 0xd8, 0)
	run(t, cpu, 1_500_000)
	if chip.RAM[im.state] != 0x15 {
		t.Fatalf("%s: stream state %#02x, want playing", im.name, chip.RAM[im.state])
	}
	return cpu, chip
}

// pathResets returns the positions in the write log where the playback path
// was reset and put back in PCM mode.
func pathResets(raw []string) []int {
	var at []int
	last := ""
	for i, w := range raw {
		if !strings.HasPrefix(w, "07:05=") {
			continue
		}
		if last == "07:05=02" && w == "07:05=50" {
			at = append(at, i)
		}
		last = w
	}
	return at
}

// A reset that comes back with the buffer full is repeated, a limited number
// of times, before the output is unmuted.
func TestRecentreRepeatsWhenFull(t *testing.T) {
	for _, im := range fixedImages(t) {
		cpu, chip := start(t, im, 384000, 0x98, 1) // 408: full at this rate
		for i := 0; i < 2*second; i++ {
			if err := cpu.Step(); err != nil {
				t.Fatal(err)
			}
			if chip.Regs[0][0x95] == 3 { // the third reset lands in the middle
				chip.Regs[7][0x1d], chip.Regs[7][0x1e] = 0xa8, 0
			}
		}
		if chip.Regs[0][0x95] != 3 || chip.Regs[0][0x96] != 0 || chip.RAM[im.state] != 0x15 || chip.Slave[7]&3 != 0 {
			t.Errorf("%s: resets before unmute %d, from the watch %d, state %#02x, DAC register 7 %#02x", im.name, chip.Regs[0][0x95], chip.Regs[0][0x96], chip.RAM[im.state], chip.Slave[7])
		}
		// And when it never comes right, the stream still starts.
		cpu, chip = start(t, im, 384000, 0x98, 1)
		for i := 0; i < 2*second && chip.Regs[0][0x96] == 0; i++ {
			if err := cpu.Step(); err != nil {
				t.Fatal(err)
			}
		}
		if chip.Regs[0][0x95] != 4 || chip.RAM[im.state] != 0x15 || chip.Slave[7]&3 != 0 {
			t.Errorf("%s, stuck full: %d resets before unmute, state %#02x, DAC register 7 %#02x", im.name, chip.Regs[0][0x95], chip.RAM[im.state], chip.Slave[7])
		}
	}
}

// The buffer is re-centred once per stream start, after the settling delay
// and before the output is unmuted.
func TestBufferRecentredBeforeUnmute(t *testing.T) {
	for _, im := range fixedImages(t) {
		recentredBeforeUnmute(t, im)
	}
}

func recentredBeforeUnmute(t *testing.T, im fixed) {
	_, chip := playing(t, im)
	resets := pathResets(chip.Raw)
	if len(resets) != 1 || chip.Regs[0][0x95] != 1 || chip.Regs[0][0x96] != 0 {
		t.Fatalf("%d path resets, counters %d and %d; want one, 1 and 0", len(resets), chip.Regs[0][0x95], chip.Regs[0][0x96])
	}
	unmuted := -1
	for i, w := range chip.Raw {
		if strings.HasPrefix(w, "02:09=") {
			v, _ := strconv.ParseUint(w[6:], 16, 8)
			if v&0x02 == 0 && i > resets[0] {
				unmuted = i
				break
			}
		}
	}
	modeSet := -1
	for i, w := range chip.Raw[:resets[0]-1] {
		if w == "07:05=50" {
			modeSet = i
		}
	}
	if modeSet < 0 || unmuted < 0 {
		t.Fatalf("PCM mode set at %d, reset at %d, mute pin released at %d", modeSet, resets[0], unmuted)
	}
	if chip.Slave[7]&3 != 0 || chip.Slave[1] != 0x80 {
		t.Errorf("DAC register 7 = %#02x, register 1 = %#02x after the start", chip.Slave[7], chip.Slave[1])
	}
	if chip.Bank != 0 && chip.Regs[7][0x05] != 0x50 {
		t.Errorf("path control left at %#02x", chip.Regs[7][0x05])
	}
}

// A playing stream whose buffer level reads pinned is re-centred from the
// main loop; a healthy or idle one is left alone.
func TestLevelWatch(t *testing.T) {
	for _, c := range []struct {
		name     string
		low, bit byte
		want     bool
	}{
		{"empty", 0x00, 0, true},
		{"nearly empty", 0x47, 0, true},
		{"low but working", 0x48, 0, false},
		{"middle", 0xf0, 0, false},
		{"middle, above 255", 0x08, 1, false},
		{"high but working", 0xbb, 1, false},
		{"nearly full", 0xbc, 1, true},
		{"full", 0xe0, 1, true},
	} {
		for _, im := range fixedImages(t) {
			cpu, chip := playing(t, im)
			before := len(pathResets(chip.Raw))
			chip.Regs[7][0x1d], chip.Regs[7][0x1e] = c.low, c.bit
			run(t, cpu, 100_000)
			got := len(pathResets(chip.Raw)) - before
			t.Logf("%-8s %-18s level %3d: %d resets in 100 ms", im.name, c.name, int(c.bit)<<8|int(c.low), got)
			if (got > 0) != c.want || int(chip.Regs[0][0x96]) != got {
				t.Errorf("%s, %s: %d resets, counter %d", im.name, c.name, got, chip.Regs[0][0x96])
			}
			if chip.RAM[im.state] != 0x15 || chip.Regs[7][0x05] != 0x50 {
				t.Errorf("%s, %s: state %#02x, path control %#02x afterwards", im.name, c.name, chip.RAM[im.state], chip.Regs[7][0x05])
			}
		}
	}
	// Not playing: the level is whatever it is and nobody cares.
	for _, im := range fixedImages(t) {
		cpu, chip := playing(t, im)
		chip.Regs[3][0x18] &^= 0x08
		run(t, cpu, 1_500_000)
		before := len(pathResets(chip.Raw))
		chip.Regs[7][0x1d], chip.Regs[7][0x1e] = 0, 0
		run(t, cpu, 200_000)
		if n := len(pathResets(chip.Raw)) - before; n != 0 || chip.Regs[0][0x96] != 0 {
			t.Errorf("%s, idle: %d resets", im.name, n)
		}
	}
}
