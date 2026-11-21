package emu

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

const (
	identityMarker = "CTUSB_Table01"
	identitySize   = 243
	origPath       = "../build/orig.bin"
	// SHA-256 of the shipped reference image (CT7601_ES9018K2M, built
	// Sep 14 2021) with its identity table left out. The addresses these
	// tests use are that image's.
	origCodeSum = "75699b980f81ef12c74180a68e9e0b0fea667ee5e111d775fe657e653b050ada"
)

// original returns the shipped image if `make test STOCK=...` put one in
// place and it is the build these tests know, and nil otherwise.
func originalImage() []byte {
	img, err := os.ReadFile(origPath)
	if err != nil {
		return nil
	}
	at := strings.Index(string(img), identityMarker)
	if at < 0 || at+identitySize > len(img) {
		return nil
	}
	sum := sha256.Sum256(append(append([]byte{}, img[:at]...), img[at+identitySize:]...))
	if hex.EncodeToString(sum[:]) != origCodeSum {
		return nil
	}
	return img
}

// readImage loads a firmware image, skipping the test if it is not there.
// The USB identity is a build setting, so an image compared with the
// original is first given the original's: the table both keep behind the
// same marker.
func readImage(t *testing.T, path string) []byte {
	t.Helper()
	orig := originalImage()
	if path == origPath {
		if orig == nil {
			t.Skip("the shipped firmware is not available: make test STOCK=your-flash-dump.bin")
		}
		return orig
	}
	img, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("%v (run make test in fw/)", err)
	}
	if orig != nil {
		from, to := strings.Index(string(orig), identityMarker), strings.Index(string(img), identityMarker)
		if to >= 0 {
			copy(img[to:to+identitySize], orig[from:from+identitySize])
		}
	}
	return img
}

// boot runs an image from reset for a fixed number of instructions.
func boot(t *testing.T, path string, steps int) (*CPU, *Chip, map[uint16]int) {
	t.Helper()
	img := readImage(t, path)
	chip := &Chip{Addr: 0x90}
	cpu := &CPU{Bus: chip}
	chip.CPU = cpu
	copy(cpu.Code[:], img)
	hot := map[uint16]int{}
	for i := 0; i < steps; i++ {
		if i >= steps-20000 {
			hot[cpu.PC]++
		}
		if err := cpu.Step(); err != nil {
			t.Fatalf("after %d instructions: %v", i, err)
		}
	}
	return cpu, chip, hot
}

// bootPrompt is boot with a host that agrees high speed 150 ms after the
// firmware attaches, as real ones do. Without it every image spends its
// first seconds waiting and re-attaching, the fixed ones longest.
func bootPrompt(t *testing.T, path string, steps int) (*CPU, *Chip, map[uint16]int) {
	t.Helper()
	img := readImage(t, path)
	chip := &Chip{Addr: 0x90}
	cpu := &CPU{Bus: chip}
	chip.CPU = cpu
	copy(cpu.Code[:], img)
	attached := -1
	for i := 0; i < steps; i++ {
		if err := cpu.Step(); err != nil {
			t.Fatalf("after %d instructions: %v", i, err)
		}
		if attached < 0 && chip.Regs[0][0x30]&0x02 != 0 {
			attached = i
		}
		if attached >= 0 && i == attached+150_000 {
			chip.Regs[0][0x32] |= 0x10
		}
	}
	if chip.Regs[0][0xad] == 0 {
		t.Fatalf("%s: not in its main loop after %d instructions", path, steps)
	}
	return cpu, chip, nil
}

func TestBootOriginal(t *testing.T) {
	cpu, chip, hot := boot(t, origPath, 4_000_000)
	var pcs []uint16
	for pc := range hot {
		pcs = append(pcs, pc)
	}
	sort.Slice(pcs, func(i, j int) bool { return pcs[i] < pcs[j] })
	t.Logf("uart: %q", cpu.UART)
	t.Logf("%d register writes, %d events", len(chip.Raw), len(chip.Log))
	t.Logf("spinning in %04x-%04x (%d addresses)", pcs[0], pcs[len(pcs)-1], len(pcs))
	if os.Getenv("BOOT_TRACE") != "" {
		fmt.Println(strings.Join(chip.Log, "\n"))
	}
}

// initEvents is what an image does before its main loop starts publishing
// stream status in register 0xa4.
func initEvents(log []string) []string {
	for i, e := range log {
		if strings.HasPrefix(e, "reg 00:a4=") {
			return log[:i]
		}
	}
	return log
}

// While the port is incomplete its start-up must at least be a prefix-free
// subset of the original's, in the same order: nothing extra, nothing
// different. The test reports how much is covered.
func TestBootPort(t *testing.T) {
	_, a, _ := boot(t, origPath, 4_000_000)
	_, b, _ := boot(t, "../build/port/port.bin", 4_000_000)
	want, got := initEvents(a.Log), initEvents(b.Log)
	i, missing := 0, []string{}
	for _, e := range got {
		start := i
		for i < len(want) && want[i] != e {
			i++
		}
		if i == len(want) {
			t.Fatalf("port event %q (after %d matched) does not occur in the original's start-up from position %d on", e, len(got), start)
		}
		missing = append(missing, want[start:i]...)
		i++
	}
	missing = append(missing, want[i:]...)
	// Past start-up both sit in their main loops: the first passes mute the
	// playback path, then status is published on every pass.
	const idle = 600
	if len(a.Log) < idle || len(b.Log) < idle {
		t.Fatalf("too few events: original %d, port %d", len(a.Log), len(b.Log))
	}
	for k := 0; k < idle; k++ {
		if a.Log[k] != b.Log[k] {
			t.Fatalf("event %d: original %q, port %q", k, a.Log[k], b.Log[k])
		}
	}
	t.Logf("port reproduces %d of the original's %d start-up events", len(got), len(want))
	if len(missing) > 0 {
		t.Logf("not yet ported: %s", strings.Join(missing, " "))
	}
	if string(a.RAM[0x2688:0x26c0]) != string(b.RAM[0x2688:0x26c0]) {
		t.Error("DSD pattern buffer differs")
	}
	// USB descriptors from 0x1800, then the sample-rate table, clock and
	// volume answers.
	for addr := 0x1800; addr < 0x2200; addr++ {
		if a.RAM[addr] != b.RAM[addr] {
			t.Errorf("descriptor RAM differs at %#x: original %02x, port %02x", addr, a.RAM[addr], b.RAM[addr])
			break
		}
	}
}

// run advances a booted machine.
func run(t *testing.T, cpu *CPU, steps int) {
	t.Helper()
	for i := 0; i < steps; i++ {
		if err := cpu.Step(); err != nil {
			t.Fatal(err)
		}
	}
}

// The whole thing end to end: the host picks an alternate setting and a
// sampling frequency, samples start arriving, then stop. Heartbeat and status
// writes are left out of the comparison because the two images get round
// their main loops at different speeds.
func TestPlaybackSession(t *testing.T) {
	type step struct {
		name string
		do   func(cpu *CPU, chip *Chip)
		run  int
	}
	request := func(s0 byte, valueLow, valueHigh, indexLow, entity byte) func(*CPU, *Chip) {
		return func(cpu *CPU, chip *Chip) {
			chip.Regs[0][0x10] |= s0
			chip.Record(valueLow, valueHigh, indexLow, entity)
			cpu.IRQ0 = true
		}
	}
	setHz := func(hz uint32) func(*CPU, *Chip) {
		return func(cpu *CPU, chip *Chip) {
			chip.RAM[0x2000], chip.RAM[0x2001], chip.RAM[0x2002], chip.RAM[0x2003] = byte(hz), byte(hz>>8), byte(hz>>16), byte(hz>>24)
			request(0x02, 0, 1, 0, 0x05)(cpu, chip)
		}
	}
	data := func(on bool) func(*CPU, *Chip) {
		return func(cpu *CPU, chip *Chip) {
			chip.Regs[3][0x18] &^= 0x08
			if on {
				chip.Regs[3][0x18] |= 0x08
			}
		}
	}
	script := []step{
		{"bus reset", request(0x40, 0, 0, 0, 0), 600_000},
		{"48 kHz", setHz(48000), 600_000},
		{"alt 3 (32-bit)", request(0x04, 3, 0, 1, 0), 600_000},
		{"samples arrive", data(true), 1_500_000},
		{"volume", func(cpu *CPU, chip *Chip) {
			copy(chip.RAM[0x21c4:], []byte{0x00, 0xf6}) // master -10 dB
			request(0x02, 0, 2, 0, 0x03)(cpu, chip)
		}, 600_000},
		{"samples stop", data(false), 1_500_000},
		{"96 kHz", setHz(96000), 600_000},
		{"alt 1 (16-bit)", request(0x04, 1, 0, 1, 0), 600_000},
		{"samples arrive", data(true), 1_500_000},
		{"mute", func(cpu *CPU, chip *Chip) {
			chip.RAM[0x21c0] = 1
			request(0x02, 0, 1, 0, 0x03)(cpu, chip)
		}, 600_000},
		{"samples stop", data(false), 1_500_000},
		// 176.4 kHz and up go through the DoP probe before PCM starts.
		{"192 kHz", setHz(192000), 600_000},
		{"alt 3 (32-bit)", request(0x04, 3, 0, 1, 0), 600_000},
		{"samples arrive", data(true), 2_500_000},
		{"samples stop", data(false), 1_500_000},
		{"176.4 kHz", setHz(176400), 600_000},
		{"samples with DoP markers", func(cpu *CPU, chip *Chip) { chip.Regs[3][0x18] |= 0x0a }, 2_500_000},
		{"markers stop", func(cpu *CPU, chip *Chip) { chip.Regs[3][0x18] &^= 0x02 }, 2_500_000},
		{"samples stop", data(false), 1_500_000},
		{"alt 4 (DSD), 176.4 kHz", request(0x04, 4, 0, 1, 0), 600_000},
		{"samples arrive", data(true), 2_500_000},
		{"samples stop", data(false), 1_500_000},
	}
	filtered := func(log []string) []string {
		var out []string
		skip := false
		for _, e := range log {
			if skip { // the path control value put back after a buffer reset
				skip = false
				continue
			}
			switch {
			// Heartbeat, status and interrupt counters differ in timing.
			case strings.HasPrefix(e, "reg 00:a"):
			// 0x9c/0x9d are speed debug bytes the vendor code writes to
			// the wrong bank and the robust build to the right one.
			case strings.Contains(e, ":9c="), strings.Contains(e, ":9d="):
			// What the fixed images add on purpose: the DAC told its
			// input format, and the buffer re-centred before unmuting.
			case strings.HasPrefix(e, "i2c 90[01]="), strings.HasPrefix(e, "reg 00:95="):
			case e == "reg 07:05=02":
				skip = true
			default:
				out = append(out, e)
			}
		}
		return out
	}
	// The fixed images are included: their fixes must not change what a
	// well-behaved session looks like beyond the two additions above.
	for _, other := range []string{"../build/port/port.bin", "../build/robust/robust.bin"} {
		ca, a, _ := bootPrompt(t, origPath, 3_000_000)
		cb, b, _ := bootPrompt(t, other, 3_000_000)
		a.Regs[7][0x1d], b.Regs[7][0x1d] = 0xd8, 0xd8 // a healthy buffer level
		a.Log, b.Log = nil, nil
		for _, s := range script {
			s.do(ca, a)
			s.do(cb, b)
			run(t, ca, s.run)
			run(t, cb, s.run)
			fa, fb := filtered(a.Log), filtered(b.Log)
			if s.name == "bus reset" && len(fb) > len(fa) && slicesEqual(fa, fb[:len(fa)]) && onlyMutes(fb[len(fa):]) {
				fb = fa // the robust build mutes at a bus reset instead of waiting for the data to stop
			}
			if !slicesEqual(fa, fb) {
				t.Fatalf("%s after %q:\noriginal: %s\nother:    %s", other, s.name, strings.Join(fa, " "), strings.Join(fb, " "))
			}
			if string(ca.UART) != string(cb.UART) {
				t.Fatalf("%s after %q: debug output %q, other %q", other, s.name, ca.UART, cb.UART)
			}
			t.Logf("%-26s %3d events, uart %q", s.name, len(fa), ca.UART)
			a.Log, b.Log = nil, nil
			ca.UART, cb.UART = nil, nil
		}
	}
}

// onlyMutes reports whether every entry is part of muting the playback path.
func onlyMutes(log []string) bool {
	for _, e := range log {
		switch {
		case e == "reg 03:01=01", e == "reg 03:1e=08", e == "reg 03:21=80", strings.HasPrefix(e, "reg 02:09="):
		case e == "i2c 90[07]=83", e == "i2c 90[0f]=ff", e == "i2c 90[10]=ff":
		default:
			return false
		}
	}
	return true
}

func slicesEqual(x, y []string) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// The port must not use MOVX @Ri: its high address byte would come from P2,
// and nothing shows this chip pages external RAM that way.
func TestPortAvoidsPagedAccess(t *testing.T) {
	cpu, _, _ := boot(t, "../build/port/port.bin", 3_000_000)
	if cpu.Paged != 0 {
		t.Errorf("port executed %d MOVX @Ri instructions", cpu.Paged)
	}
}

// A byte arriving on the serial port must not derail either image.
func TestSerialInterrupt(t *testing.T) {
	for _, path := range []string{origPath, "../build/port/port.bin"} {
		cpu, chip, _ := bootPrompt(t, path, 3_000_000)
		before := len(chip.Log)
		cpu.SFR[sfrSCON-0x80] |= 0x01 // RI
		run(t, cpu, 200_000)
		if cpu.SFR[sfrSCON-0x80]&0x01 != 0 {
			t.Errorf("%s: receive flag still set", path)
		}
		if len(chip.Log) == before {
			t.Errorf("%s: main loop stopped after a received byte", path)
		}
	}
}

// symbolsOf reads an SDCC link map.
func symbolsOf(t *testing.T, mapPath string) map[string]uint16 {
	t.Helper()
	data, err := os.ReadFile(mapPath)
	if err != nil {
		t.Skipf("%v (run make test in fw/)", err)
	}
	sym := map[string]uint16{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && (f[0] == "C:" || f[0] == "D:") && strings.HasPrefix(f[2], "_") {
			var v uint32
			fmt.Sscanf(f[1], "%X", &v)
			sym[f[2][1:]] = uint16(v)
		}
	}
	return sym
}

// The vendor firmware's alternate-setting race, as measured on a real unit:
// the host sets the sampling frequency and, a moment later, selects the
// 32-bit alternate setting. If that lands while the frequency change is
// still being applied, the firmware finishes by writing the old alternate
// setting back and stays framed for 16-bit samples.
//
// The original image and the faithful port must both show it (the port is
// supposed to be the same firmware). The robust build must not.
func TestAlternateSettingRace(t *testing.T) {
	type image struct {
		name, path string
		playAlt    uint16
		wantStale  bool
	}
	images := []image{
		{"port", "../build/port/port.bin", symbolsOf(t, "../build/port/port.map")["play_alt"], true},
		{"robust", "../build/robust/robust.bin", symbolsOf(t, "../build/robust/robust.map")["play_alt"], false},
	}
	if originalImage() != nil {
		images = append([]image{{"original", origPath, 0x27d9, true}}, images...)
	}
	for _, im := range images {
		stale, tried := 0, 0
		for gap := 0; gap <= 12000; gap += 400 {
			cpu, chip, _ := bootPrompt(t, im.path, 2_000_000)
			hz := uint32(48000)
			chip.RAM[0x2000], chip.RAM[0x2001], chip.RAM[0x2002], chip.RAM[0x2003] = byte(hz), byte(hz>>8), byte(hz>>16), byte(hz>>24)
			chip.Regs[0][0x10] |= 0x02
			chip.Record(0, 1, 0, 0x05)
			cpu.IRQ0 = true
			run(t, cpu, gap)
			chip.Regs[0][0x10] |= 0x04
			chip.Record(3, 0, 1, 0)
			cpu.IRQ0 = true
			run(t, cpu, 900_000)
			tried++
			if chip.RAM[im.playAlt]&0x0f != 3 {
				stale++
			}
		}
		t.Logf("%-9s kept the old alternate setting in %d of %d timings", im.name, stale, tried)
		if im.wantStale && stale == 0 {
			t.Errorf("%s: expected to reproduce the vendor race", im.name)
		}
		if !im.wantStale && stale != 0 {
			t.Errorf("%s: lost the alternate setting in %d of %d timings", im.name, stale, tried)
		}
	}
}

// robustImages pairs the vendor image with the robust build for tests of
// behaviour the robust build deliberately changes.
type variant struct {
	name, path string
	sym        map[string]uint16
	robust     bool
}

// variants is the shipped image, when there is one to compare with, and the
// robust build.
func variants(t *testing.T) []variant {
	v := []variant{{"robust", "../build/robust/robust.bin", symbolsOf(t, "../build/robust/robust.map"), true}}
	if originalImage() != nil {
		v = append([]variant{{"original", origPath, originalSymbols, false}}, v...)
	}
	return v
}

// robustVariant is the robust build alone.
func robustVariant(t *testing.T) variant {
	v := variants(t)
	return v[len(v)-1]
}

// DAC writes that are not acknowledged. The vendor firmware never notices and
// leaves the DAC at its power-on defaults; the robust build retries.
func TestDACInitSurvivesNacks(t *testing.T) {
	for _, v := range variants(t) {
		m := newMachine(t, v.path, v.sym, !v.robust)
		m.chip.Nack = 4 // the first four bytes: the first write, and part of the second
		m.call(t, "dac_init")
		bad := 0
		for _, r := range [][2]byte{{0x00, 0xf0}, {0x01, 0x8c}, {0x07, 0x83}, {0x0c, 0xbe}, {0x0f, 0xff}} {
			if m.chip.Slave[r[0]] != r[1] {
				bad++
			}
		}
		t.Logf("%-8s registers left wrong after 4 unacknowledged bytes: %d", v.name, bad)
		if v.robust && bad != 0 {
			t.Errorf("robust build left %d DAC registers unset", bad)
		}
		if !v.robust && bad == 0 {
			t.Error("expected the vendor image to lose writes; the test is not exercising anything")
		}
	}
}

// After a stream has played and stopped, a host "unmute" request must not open
// the output while nothing is playing.
func TestHostUnmuteWhileIdle(t *testing.T) {
	for _, v := range variants(t) {
		cpu, chip, _ := bootPrompt(t, v.path, 2_000_000)
		step := func(s0, valueLow, valueHigh, indexLow, entity byte, steps int) {
			chip.Regs[0][0x10] |= s0
			chip.Record(valueLow, valueHigh, indexLow, entity)
			cpu.IRQ0 = true
			run(t, cpu, steps)
		}
		copy(chip.RAM[0x2000:], []byte{0x80, 0xbb, 0x00, 0x00})
		step(0x02, 0, 1, 0, 0x05, 600_000) // 48 kHz
		step(0x04, 3, 0, 1, 0, 600_000)    // 32-bit
		chip.Regs[3][0x18] |= 0x08
		run(t, cpu, 1_500_000)
		if chip.Slave[0x07]&0x03 != 0 {
			t.Fatalf("%s: not unmuted while playing", v.name)
		}
		chip.Regs[3][0x18] &^= 0x08
		run(t, cpu, 1_500_000)
		if chip.Slave[0x07]&0x03 != 0x03 {
			t.Fatalf("%s: not muted after the stream stopped", v.name)
		}
		chip.RAM[0x21c0] = 0
		step(0x02, 0, 1, 0, 0x03, 1_500_000) // host: mute off
		open := chip.Slave[0x07]&0x03 == 0 || chip.Regs[2][0x09]&0x02 == 0
		t.Logf("%-8s output open after an idle unmute request: %v", v.name, open)
		if v.robust && open {
			t.Error("robust build unmuted with nothing playing")
		}
		if !v.robust && !open {
			t.Error("expected the vendor image to unmute; the test is not exercising anything")
		}
	}
}

// A second interrupt cause arriving while the handler runs, on a chip that
// holds its interrupt line until every cause is acknowledged (so there is no
// new edge). The vendor handler never sees it.
func TestInterruptCauseDuringHandler(t *testing.T) {
	for _, v := range variants(t) {
		cpu, chip, _ := bootPrompt(t, v.path, 2_000_000)
		copy(chip.RAM[0x2000:], []byte{0x80, 0xbb, 0x00, 0x00})
		chip.OnAck = func() { // the alternate-setting change lands just as the first cause is acknowledged
			chip.Regs[0][0x10] |= 0x04
			chip.Record(3, 0, 1, 0)
		}
		chip.Regs[0][0x10] |= 0x02
		chip.Record(0, 1, 0, 0x05)
		cpu.IRQ0 = true
		run(t, cpu, 1_200_000)
		alt := chip.RAM[v.sym["play_alt"]] & 0x0f
		rate := chip.RAM[v.sym["play_alt"]] >> 4
		t.Logf("%-8s alternate setting applied: %d, rate index %d, status left pending: %02x", v.name, alt, rate, chip.Regs[0][0x10])
		if v.robust && alt != 3 {
			t.Errorf("robust build missed the second cause (alt %d)", alt)
		}
		// The frequency request's record was replaced before it was read;
		// the rate is picked up from the chip's answer buffer instead.
		if v.robust && rate != 1 {
			t.Errorf("robust build did not apply the rate (index %d)", rate)
		}
		if !v.robust && alt == 3 {
			t.Error("expected the vendor image to miss it; the test is not exercising anything")
		}
	}
}

// The robust build must never drive the mute pin low before the DAC has been
// configured and a stream is running.
func TestMutePinDuringStartup(t *testing.T) {
	for _, v := range variants(t) {
		_, chip, _ := bootPrompt(t, v.path, 2_000_000)
		low := 0
		for _, e := range chip.Raw {
			if strings.HasPrefix(e, "02:09=") {
				var val int
				fmt.Sscanf(e[6:], "%x", &val)
				if val&0x02 == 0 {
					low++
				}
			}
		}
		t.Logf("%-8s writes with the mute pin low during start-up: %d", v.name, low)
		if v.robust && low != 0 {
			t.Errorf("robust build drove the mute pin low %d times", low)
		}
		if !v.robust && low == 0 {
			t.Error("expected the vendor image to drive it low; the test is not exercising anything")
		}
	}
}

// Coming back from the powered-down wait, the robust build treats the DAC as
// possibly reset: it rewrites and checks its registers and restarts the
// stream state machine muted.
func TestResumeReconfiguresDAC(t *testing.T) {
	for _, v := range variants(t) {
		m := newMachine(t, v.path, v.sym, !v.robust)
		m.call(t, "dac_init")
		for reg := range m.chip.Slave { // the DAC lost power: back to its own defaults
			m.chip.Slave[reg] = 0
		}
		m.chip.Slave[0x01], m.chip.Slave[0x07], m.chip.Slave[0x0c] = 0x8c, 0x80, 0x5a
		m.chip.Regs[0][0x33] |= 0x01
		m.chip.Regs[0][0x32] = 0x00
		m.set(t, "power_state", 9)
		m.call(t, "power_service")
		// The robust build waits until the bus has been re-entered.
		m.chip.Regs[0][0x32] = 0x02
		for i := 0; i < 30; i++ {
			m.call(t, "power_service")
		}
		restored := m.chip.Slave[0x0c] == 0xbe && m.chip.Slave[0x0e] == 0x8a
		muted := m.chip.Slave[0x07]&0x03 == 0x03
		t.Logf("%-8s after resume: DAC settings restored %v, muted %v", v.name, restored, muted)
		if v.robust && (!restored || !muted) {
			t.Error("robust build did not bring the DAC back")
		}
		if !v.robust && restored {
			t.Error("expected the vendor image to leave the DAC as it was; the test is not exercising anything")
		}
	}
}
