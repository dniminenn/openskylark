package emu

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// machine is one firmware image loaded into the emulator, with the calling
// convention its compiler uses.
type machine struct {
	cpu  *CPU
	chip *Chip
	sym  map[string]uint16
	keil bool
}

// Where things are in the shipped image.
var originalSymbols = map[string]uint16{
	"timer0_init": 0x53a2, "reg_update": 0x515f, "gpio_set": 0x513b,
	"i2c_init": 0x5051, "dac_init": 0x5372, "dac_set_volume": 0x51e4,
	"dac_mute":         0x4b91,
	"descriptors_init": 0x4ad4, "cfg": 0x27d8,
	"rate_table_init": 0x0efa, "rate_mask": 0x27f0, "volume_range_init": 0x1bc0,
	"control_table_init": 0x0026,
	"int0_isr":           0x20fd, "events_a": 0x27dc, "events_b": 0x27dd, "irq_seen": 0x2787,
	"pending": 0x2786, "power_events": 0x2801, "aux_flags": 0x27fb, "play_alt": 0x27d9,
	"request_entity":    0x27df,
	"stream_format_pcm": 0x525e, "stream_format_dop": 0x527b, "stream_format_dsd": 0x5298,
	"stream_format": 0x2805, "volume_left": 0x2803, "volume_right": 0x2804,
}

func newMachine(t *testing.T, path string, sym map[string]uint16, keil bool) *machine {
	t.Helper()
	img := readImage(t, path)
	m := &machine{cpu: &CPU{}, chip: &Chip{Addr: 0x90}, sym: sym, keil: keil}
	m.cpu.Bus, m.chip.CPU = m.chip, m.cpu
	copy(m.cpu.Code[:], img)
	m.cpu.SFR[sfrSP-0x80] = 0x60
	m.call(t, "timer0_init")      // delay() waits on the timer interrupt
	m.cpu.SFR[sfrIE-0x80] |= 0x90 // interrupts on, serial included, as after start-up
	m.cpu.SFR[sfrSCON-0x80] = 0x50
	return m
}

func original(t *testing.T) *machine {
	return newMachine(t, origPath, originalSymbols, true)
}

func port(t *testing.T) *machine {
	t.Helper()
	f, err := os.Open("../build/port/port.map")
	if err != nil {
		t.Skipf("%v (run make test in fw/)", err)
	}
	defer f.Close()
	sym := map[string]uint16{}
	line := regexp.MustCompile(`^[CD]:\s+([0-9A-F]{8})\s+_(\w+)`)
	for sc := bufio.NewScanner(f); sc.Scan(); {
		if m := line.FindStringSubmatch(sc.Text()); m != nil {
			v, _ := strconv.ParseUint(m[1], 16, 32)
			sym[m[2]] = uint16(v)
		}
	}
	return newMachine(t, "../build/port/port.bin", sym, false)
}

// addr resolves a symbol, optionally with "+offset".
func (m *machine) addr(t *testing.T, name string) uint16 {
	t.Helper()
	if base, off, found := strings.Cut(name, "+"); found {
		n, _ := strconv.Atoi(off)
		return m.addr(t, base) + uint16(n)
	}
	a, ok := m.sym[name]
	if !ok {
		t.Fatalf("no symbol %q", name)
	}
	return a
}

// call passes up to three byte arguments the way each compiler expects:
// Keil in r7, r5, r3; SDCC in dpl and then per-function parameter variables.
func (m *machine) call(t *testing.T, name string, args ...byte) {
	t.Helper()
	for i, a := range args {
		switch {
		case m.keil:
			m.cpu.SetR(byte(7-2*i), a)
		case i == 0:
			m.cpu.SFR[sfrDPL-0x80] = a
		default:
			m.chip.RAM[m.addr(t, name+"_PARM_"+strconv.Itoa(i+1))] = a
		}
	}
	if err := m.cpu.Call(m.addr(t, name), 5_000_000); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func (m *machine) set(t *testing.T, name string, v byte) {
	t.Helper()
	m.chip.RAM[m.addr(t, name)] = v
}

// same runs a scenario on both images and requires identical register and
// I2C activity, down to each bank switch.
func same(t *testing.T, scenario func(t *testing.T, m *machine)) {
	t.Helper()
	a, b := original(t), port(t)
	scenario(t, a)
	scenario(t, b)
	if !slices.Equal(a.chip.Raw, b.chip.Raw) {
		t.Errorf("register writes differ\noriginal: %s\nport:     %s", strings.Join(a.chip.Raw, " "), strings.Join(b.chip.Raw, " "))
	}
	if a.chip.Slave != b.chip.Slave {
		t.Error("DAC registers differ")
	}
	if len(a.chip.Raw) == 0 {
		t.Error("scenario wrote nothing")
	}
	t.Logf("%d register writes, %d logged events: %s", len(a.chip.Raw), len(a.chip.Log), strings.Join(a.chip.Log, " "))
}

func TestOriginalDACInitMatchesDevice(t *testing.T) {
	m := original(t)
	m.call(t, "dac_init")
	// What the test unit reports for these DAC registers when idle.
	want := map[byte]byte{0x00: 0xf0, 0x01: 0x8c, 0x04: 0x0a, 0x05: 0xee, 0x06: 0x4a, 0x07: 0x83, 0x08: 0x10, 0x0b: 0x02, 0x0c: 0xbe, 0x0e: 0x8a, 0x0f: 0xff, 0x10: 0xff, 0x14: 0x66}
	for reg, v := range want {
		if m.chip.Slave[reg] != v {
			t.Errorf("DAC register %#02x = %#02x, want %#02x", reg, m.chip.Slave[reg], v)
		}
	}
}

func TestRegUpdate(t *testing.T) {
	same(t, func(t *testing.T, m *machine) {
		m.chip.Regs[0][0x30] = 0xa5
		m.call(t, "reg_update", 0x30, 0x0f, 0x3c)
	})
}

func TestGPIOSet(t *testing.T) {
	same(t, func(t *testing.T, m *machine) {
		m.chip.Bank = 0x05
		m.chip.Regs[2][0x09] = 0x0f
		m.call(t, "gpio_set", 0x20, 1)
		m.call(t, "gpio_set", 0x01, 0)
	})
}

func TestI2CInit(t *testing.T) {
	same(t, func(t *testing.T, m *machine) {
		m.chip.Regs[2][0x08] = 0xff
		m.call(t, "i2c_init")
	})
}

func TestDACInit(t *testing.T) {
	same(t, func(t *testing.T, m *machine) { m.call(t, "dac_init") })
}

func TestDACVolume(t *testing.T) {
	same(t, func(t *testing.T, m *machine) { m.call(t, "dac_set_volume", 0x28, 0x30) })
}

func TestDACMute(t *testing.T) {
	same(t, func(t *testing.T, m *machine) {
		m.chip.Slave[0x07] = 0x80
		m.call(t, "dac_mute", 1)
	})
}

func TestDACUnmute(t *testing.T) {
	same(t, func(t *testing.T, m *machine) {
		m.chip.Slave[0x07] = 0x83
		m.chip.Regs[2][0x09] = 0x2f
		m.set(t, "stream_format", 0x11)
		m.set(t, "volume_left", 0x28)
		m.set(t, "volume_right", 0x2a)
		m.call(t, "dac_mute", 0)
	})
}

// With nothing playing the original leaves the DAC muted, and so must we.
func TestDACUnmuteWhileIdle(t *testing.T) {
	a, b := original(t), port(t)
	for _, m := range []*machine{a, b} {
		m.set(t, "stream_format", 0)
		m.call(t, "dac_mute", 0)
		if len(m.chip.Raw) != 0 {
			t.Errorf("wrote registers while idle: %v", m.chip.Raw)
		}
	}
}

// identityTable is the 243-byte block at X:2682 with the given text fields.
func identityTable(fields [7]string) []byte {
	t := []byte("CTUSB_Table01\x2f\xc6\xf8\x01")
	for i, f := range fields {
		if i == 6 {
			t = append(t, 0xf8, 0x41)
		}
		t = append(t, []byte(f+strings.Repeat(" ", 32-len(f)))...)
	}
	return t
}

func TestDescriptors(t *testing.T) {
	stock := [7]string{"Maker", "Product UAC2 384", "Product UAC2 384", "Product UAC2 384", "Product UAC2 384", "Product UAC2 384", "Maker UAC1"}
	odd := [7]string{"", "0123456789abcdefghijklmnopqrstuv", "a  b ", " x", "trailing\x00", "Z", "UAC1 name"}
	for name, c := range map[string]struct {
		cfg    byte
		fields [7]string
	}{
		"uac2":         {0x42, stock},
		"uac1":         {0x43, stock},
		"odd strings":  {0x42, odd},
		"odd and uac1": {0x43, odd},
	} {
		a, b := original(t), port(t)
		// The original reads its RAM copy of the table; the port reads the
		// table in its image.
		table := identityTable(c.fields)
		copy(a.chip.RAM[0x2682:], table)
		at := strings.Index(string(b.cpu.Code[:]), "CTUSB_Table01")
		if at < 0 {
			t.Fatal("no identity table in the port image")
		}
		copy(b.cpu.Code[at:], table)
		for _, m := range []*machine{a, b} {
			m.set(t, "cfg", c.cfg)
			m.chip.Bank = 0x03
			m.call(t, "descriptors_init")
		}
		if !slices.Equal(a.chip.Raw, b.chip.Raw) {
			t.Errorf("%s: register writes differ\noriginal: %v\nport:     %v", name, a.chip.Raw, b.chip.Raw)
		}
		used := 0
		for addr := 0x1800; addr < 0x2000; addr++ {
			if a.chip.RAM[addr] != 0 {
				used++
			}
		}
		if len(a.chip.Raw) < 50 || used < 400 {
			t.Errorf("%s: original did too little: %d register writes, %d descriptor bytes", name, len(a.chip.Raw), used)
		}
		for addr := 0x1800; addr < 0x2000; addr++ {
			if a.chip.RAM[addr] != b.chip.RAM[addr] {
				t.Errorf("%s: descriptor RAM differs at %#x: original % x, port % x", name, addr, a.chip.RAM[addr:addr+8], b.chip.RAM[addr:addr+8])
				break
			}
		}
	}
}

func sameRAM(t *testing.T, name string, a, b *machine, from, to int) {
	t.Helper()
	used := 0
	for addr := from; addr < to; addr++ {
		if a.chip.RAM[addr] != 0 {
			used++
		}
		if a.chip.RAM[addr] != b.chip.RAM[addr] {
			t.Errorf("%s: RAM differs at %#x: original % x, port % x", name, addr, a.chip.RAM[addr:addr+8], b.chip.RAM[addr:addr+8])
			return
		}
	}
	if !slices.Equal(a.chip.Raw, b.chip.Raw) {
		t.Errorf("%s: register writes differ\noriginal: %v\nport:     %v", name, a.chip.Raw, b.chip.Raw)
	}
	t.Logf("%s: %d bytes of RAM, %d register writes", name, used, len(a.chip.Raw))
}

// Every combination of supported rates the chip variants can produce, plus
// some that cannot occur.
func TestRateTable(t *testing.T) {
	for _, mask := range [][2]byte{{0x0d, 0xb6}, {0x00, 0x36}, {0x01, 0xb6}, {0x7f, 0xff}, {0x00, 0x01}, {0x40, 0x00}, {0x00, 0x00}} {
		a, b := original(t), port(t)
		for _, m := range []*machine{a, b} {
			m.set(t, "rate_mask", mask[0])
			m.chip.RAM[m.addr(t, "rate_mask")+1] = mask[1]
			if m.keil {
				m.call(t, "rate_table_init", 1)
			} else {
				m.call(t, "rate_table_init")
			}
		}
		sameRAM(t, fmt.Sprintf("mask %02x%02x", mask[0], mask[1]), a, b, 0x2000, 0x2180)
	}
}

func TestVolumeRangeAndControlTable(t *testing.T) {
	for _, cfg := range []byte{0x42, 0x43} {
		a, b := original(t), port(t)
		for _, m := range []*machine{a, b} {
			m.set(t, "cfg", cfg)
			if m.keil {
				m.call(t, "volume_range_init", 1)
			} else {
				m.call(t, "volume_range_init")
			}
			m.call(t, "control_table_init")
		}
		sameRAM(t, fmt.Sprintf("cfg %02x", cfg), a, b, 0x2000, 0x2200)
	}
}

// The interrupt handler, fed random interrupt causes and request fields. Both
// images must write the same registers and leave the same event flags.
func TestInterruptHandler(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	vars := []string{"events_a", "events_b", "irq_seen", "pending", "power_events", "aux_flags", "play_alt", "request_entity"}
	entities := []byte{0x03, 0x05, 0x0b, 0x0c, 0x0f, 0x13, 0x15, 0x00, 0x01, 0x02, 0x04, 0x07}
	for i := 0; i < 3000; i++ {
		in := [8]byte{byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(4)), byte(rng.Intn(4)), []byte{0, 1, 2, 4, 0x82}[rng.Intn(5)], entities[rng.Intn(len(entities))], byte(rng.Intn(256)), byte(rng.Intn(256))}
		a, b := original(t), port(t)
		for _, m := range []*machine{a, b} {
			m.chip.Bank = in[6] & 0x0f
			m.chip.Regs[0][0x10], m.chip.Regs[0][0x11] = in[0], in[1]
			m.chip.Regs[6][0xf2], m.chip.Regs[6][0xf3], m.chip.Regs[6][0xf4], m.chip.Regs[6][0xf5] = in[2], in[3], in[4], in[5]
			m.chip.Regs[2][0x09] = in[7]
			if i%3 == 0 {
				for _, v := range vars {
					m.set(t, v, in[7]^in[6])
				}
			}
			m.call(t, "int0_isr")
		}
		if !slices.Equal(a.chip.Raw, b.chip.Raw) {
			t.Fatalf("case %d % x: register writes differ\noriginal: %v\nport:     %v", i, in, a.chip.Raw, b.chip.Raw)
		}
		for _, v := range vars {
			if x, y := a.chip.RAM[a.addr(t, v)], b.chip.RAM[b.addr(t, v)]; x != y {
				t.Fatalf("case %d % x: %s = %02x in the original, %02x in the port", i, in, v, x, y)
			}
		}
	}
}

func init() {
	for name, addr := range map[string]uint16{
		"interface_event": 0x2f5f, "path_state": 0x277a, "path_bits": 0x277b,
		"play_flags": 0x27ea, "rec_flags": 0x27ec,
	} {
		originalSymbols[name] = addr
	}
}

// randomise gives both machines the same arbitrary register contents.
func randomise(rng *rand.Rand, a, b *machine) {
	for bank := 0; bank < 16; bank++ {
		for reg := 1; reg < 256; reg++ {
			v := byte(rng.Intn(256))
			a.chip.Regs[bank][reg], b.chip.Regs[bank][reg] = v, v
		}
	}
	for reg := range a.chip.Slave {
		v := byte(rng.Intn(256))
		a.chip.Slave[reg], b.chip.Slave[reg] = v, v
	}
	bank := byte(rng.Intn(16))
	a.chip.Bank, b.chip.Bank = bank, bank
}

type fuzzCase struct {
	ram   [][2]int // extra RAM ranges that must end up equal
	vars  []string // one byte each; set to random values, compared afterwards
	setup func(rng *rand.Rand, m [2]*machine, t *testing.T) string
	run   func(m *machine, t *testing.T)
}

// fuzz runs random cases on both images and requires the same register
// writes, debug output and variable values.
func fuzz(t *testing.T, n int, c fuzzCase) {
	t.Helper()
	rng := rand.New(rand.NewSource(7))
	writes := 0
	for i := 0; i < n; i++ {
		a, b := original(t), port(t)
		randomise(rng, a, b)
		for _, v := range c.vars {
			x := byte(rng.Intn(256))
			a.set(t, v, x)
			b.set(t, v, x)
		}
		desc := c.setup(rng, [2]*machine{a, b}, t)
		c.run(a, t)
		c.run(b, t)
		writes += len(a.chip.Raw)
		if !slices.Equal(a.chip.Raw, b.chip.Raw) {
			t.Fatalf("case %d (%s): register writes differ\noriginal: %v\nport:     %v", i, desc, a.chip.Raw, b.chip.Raw)
		}
		if string(a.cpu.UART) != string(b.cpu.UART) {
			t.Fatalf("case %d (%s): debug output %q, port %q", i, desc, a.cpu.UART, b.cpu.UART)
		}
		for _, v := range c.vars {
			if x, y := a.chip.RAM[a.addr(t, v)], b.chip.RAM[b.addr(t, v)]; x != y {
				t.Fatalf("case %d (%s): %s = %02x in the original, %02x in the port", i, desc, v, x, y)
			}
		}
		for _, r := range c.ram {
			for addr := r[0]; addr < r[1]; addr++ {
				if a.chip.RAM[addr] != b.chip.RAM[addr] {
					t.Fatalf("case %d (%s): RAM %#x = %02x in the original, %02x in the port", i, desc, addr, a.chip.RAM[addr], b.chip.RAM[addr])
				}
			}
		}
	}
	if writes < n/4 {
		t.Errorf("only %d register writes over %d cases", writes, n)
	}
}

func TestInterfaceEvent(t *testing.T) {
	rates := []uint32{44100, 48000, 88200, 96000, 176400, 192000, 352800, 384000, 705600, 768000, 32000, 12345, 0}
	which := byte(0)
	fuzz(t, 1500, fuzzCase{
		vars: []string{"cfg", "play_alt", "path_state", "path_bits", "play_flags", "rec_flags", "stream+1", "stream_format", "volume_left", "volume_right"},
		setup: func(rng *rand.Rand, m [2]*machine, t *testing.T) string {
			hz := rates[rng.Intn(len(rates))]
			alt := byte(rng.Intn(5))
			hi := byte(rng.Intn(16)) << 4
			which = byte(1 + rng.Intn(8)/7)
			for _, x := range m {
				x.chip.RAM[0x2000], x.chip.RAM[0x2001], x.chip.RAM[0x2002], x.chip.RAM[0x2003] = byte(hz), byte(hz>>8), byte(hz>>16), byte(hz>>24)
				x.set(t, "play_alt", hi|alt)
			}
			return fmt.Sprintf("which %d alt %d %d Hz", which, alt, hz)
		},
		run: func(m *machine, t *testing.T) { m.call(t, "interface_event", which) },
	})
}

func init() { originalSymbols["request_event"] = 0x39f5 }

func TestRequestEvent(t *testing.T) {
	event := byte(0)
	fuzz(t, 1500, fuzzCase{
		vars: []string{"cfg", "play_alt", "path_state", "play_flags", "rec_flags", "stream_format", "volume_left", "volume_right"},
		setup: func(rng *rand.Rand, m [2]*machine, t *testing.T) string {
			event = 1 << rng.Intn(8)
			var buf [0x150]byte
			rng.Read(buf[:])
			if rng.Intn(3) == 0 { // a plausible volume: small negative dB values
				for _, at := range []int{4, 0x14, 0x24} {
					buf[at], buf[at+1] = byte(rng.Intn(256)), 0xc0+byte(rng.Intn(64))
				}
			}
			for _, x := range m {
				copy(x.chip.RAM[0x21c0:], buf[:])
			}
			return fmt.Sprintf("event %02x, volume % x / % x / % x", event, buf[4:6], buf[0x14:0x16], buf[0x24:0x26])
		},
		run: func(m *machine, t *testing.T) { m.call(t, "request_event", event) },
	})
}

func init() {
	originalSymbols["stream_service"] = 0x0b32
	originalSymbols["stream"] = 0x27e4
	originalSymbols["elapsed_a"] = 0x27e0
}

func TestStreamStateMachine(t *testing.T) {
	states := []byte{0x00, 0x01, 0x02, 0x03, 0x11, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x24, 0x25, 0x26, 0x27, 0x28, 0x31, 0x33, 0x34, 0x35, 0x36, 0x38, 0x48, 0x4a, 0x99}
	// The elapsed counter keeps running while a state mutes the DAC, and the
	// two images take different numbers of instructions to do that. Stay
	// well clear of the thresholds (1, 10, 20, 100, 120) so both see the
	// same side of each.
	elapsed := []byte{0, 3, 22, 60, 102, 122, 200, 255}
	path := byte(0)
	fuzz(t, 6000, fuzzCase{
		vars: []string{"cfg", "play_alt", "path_state", "play_flags", "rec_flags", "stream_format", "volume_left", "volume_right", "stream+0", "stream+1", "stream+2", "stream+3", "stream+4", "stream+5"},
		setup: func(rng *rand.Rand, m [2]*machine, t *testing.T) string {
			path = byte(rng.Intn(2))
			st := states[rng.Intn(len(states))]
			el := elapsed[rng.Intn(len(elapsed))]
			hold := []byte{0, 0, 0, 1, 2}[rng.Intn(5)]
			status := byte(rng.Intn(256))
			for _, x := range m {
				x.set(t, fmt.Sprintf("stream+%d", 3*int(path)), st)
				x.set(t, fmt.Sprintf("stream+%d", 3*int(path)+1), status)
				x.set(t, fmt.Sprintf("stream+%d", 3*int(path)+2), hold)
				x.set(t, "elapsed_a", el)
			}
			return fmt.Sprintf("path %d state %02x hold %d elapsed %d", path, st, hold, el)
		},
		run: func(m *machine, t *testing.T) { m.call(t, "stream_service", path) },
	})
}

func init() {
	for name, addr := range map[string]uint16{
		"power_service": 0x341a, "flags_service": 0x48d1, "power_state": 0x2800, "resume_count": 0x2802,
	} {
		originalSymbols[name] = addr
	}
}

func TestPowerService(t *testing.T) {
	fuzz(t, 3000, fuzzCase{
		vars: []string{"cfg", "power_state", "power_events", "resume_count", "path_state"},
		setup: func(rng *rand.Rand, m [2]*machine, t *testing.T) string {
			state := byte(1 + rng.Intn(12))
			line := []byte{0, 1, 2, 3, 5}[rng.Intn(5)]
			count := []byte{0, 1, 19, 20, 21, 200}[rng.Intn(6)]
			for _, x := range m {
				x.set(t, "power_state", state)
				x.set(t, "resume_count", count)
				x.chip.Regs[0][0x32] = x.chip.Regs[0][0x32]&0xf0 | line
				if state == 9 { // the suspend wait must be able to end
					x.chip.Regs[0][0x33] |= 0x01
					if line == 3 {
						x.chip.Regs[0][0x32]--
					}
				}
			}
			return fmt.Sprintf("state %d line %d count %d", state, line, count)
		},
		run: func(m *machine, t *testing.T) {
			m.call(t, "power_service")
			if m.cpu.SFR[sfrIE-0x80]&0x80 == 0 {
				t.Error("interrupts left disabled")
			}
		},
	})
}

func TestFlagsService(t *testing.T) {
	writes := 0
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 600; i++ {
		a, b := original(t), port(t)
		randomise(rng, a, b)
		pending := byte(rng.Intn(256))
		if i%5 == 0 {
			pending = 0
		}
		for _, m := range []*machine{a, b} {
			m.set(t, "pending", pending)
			m.call(t, "flags_service")
		}
		writes += len(a.chip.Raw)
		if !slices.Equal(a.chip.Raw, b.chip.Raw) || a.chip.RAM[a.addr(t, "pending")] != b.chip.RAM[b.addr(t, "pending")] {
			t.Fatalf("pending %02x: original %v, port %v", pending, a.chip.Raw, b.chip.Raw)
		}
	}
	if writes == 0 {
		t.Error("nothing written")
	}
}

func init() {
	for name, addr := range map[string]uint16{
		"buttons_poll": 0x476d, "aux_service": 0x3307, "hid_state": 0x27fc, "hid_report": 0x27fd,
		"buttons": 0x27f4, "hold_count": 0x27f6, "long_press": 0x27fe, "usb_flags": 0x27f3,
	} {
		originalSymbols[name] = addr
	}
}

func TestButtonsPoll(t *testing.T) {
	counts := []byte{0, 1, 4, 5, 6, 8, 9, 10, 0xfe, 0xff}
	fuzz(t, 2000, fuzzCase{
		vars: []string{"usb_flags", "buttons+0", "buttons+1", "hold_count", "long_press"},
		setup: func(rng *rand.Rand, m [2]*machine, t *testing.T) string {
			count, long := counts[rng.Intn(len(counts))], byte(rng.Intn(3))
			pins := byte(rng.Intn(256))
			if rng.Intn(2) == 0 {
				pins |= 0x27 // nothing pressed
			}
			for _, x := range m {
				x.set(t, "hold_count", count)
				x.set(t, "long_press", long)
				x.chip.Regs[2][0x0c] = pins
			}
			return fmt.Sprintf("pins %02x count %d long %d", pins, count, long)
		},
		run: func(m *machine, t *testing.T) { m.call(t, "buttons_poll") },
	})
}

func TestAuxService(t *testing.T) {
	fuzz(t, 2500, fuzzCase{
		ram:  [][2]int{{0x2680, 0x2688}, {0x1800, 0x1830}},
		vars: []string{"aux_flags", "hid_state", "hid_report"},
		setup: func(rng *rand.Rand, m [2]*machine, t *testing.T) string {
			state := byte(rng.Intn(7))
			var frame [8]byte
			rng.Read(frame[:])
			switch rng.Intn(4) {
			case 0:
				copy(frame[:], []byte{0x5a, 0xb0, 0xca, 0xfe})
			case 1:
				copy(frame[:], []byte{0xc7, 0xa5, byte(1 + rng.Intn(4))})
				frame[6] = frame[0] + frame[1] + frame[2] + frame[3] + frame[4] + frame[5]
			}
			for _, x := range m {
				x.set(t, "hid_state", state)
				copy(x.chip.RAM[0x2680:], frame[:])
			}
			return fmt.Sprintf("state %d frame % x", state, frame)
		},
		run: func(m *machine, t *testing.T) { m.call(t, "aux_service") },
	})
}

func init() {
	for name, addr := range map[string]uint16{
		"mailbox_service": 0x0632, "loop_mask": 0x2789, "rec_alt": 0x27db, "aux_path_flags": 0x27ee,
		"spare_27da": 0x27da, "spare_27e3": 0x27e3,
	} {
		originalSymbols[name] = addr
	}
}

// Every host command, with parameters chosen to reach each branch. Memory
// peeks are pointed at RAM both images share.
func TestMailbox(t *testing.T) {
	commands := []byte{0x2c, 0x2e, 0x30, 0x32, 0x34, 0x38, 0x50, 0x58, 0x5a, 0x90, 0x91, 0xa0, 0xd0, 0xd2, 0xd8, 0xd9, 0xda, 0x00, 0xee, 0x31, 0x40, 0xfe}
	overrides := []byte{0xf0, 1, 2, 3, 4, 5, 6, 7, 8, 0x11, 0x12, 0x13, 0x14, 0x80, 0x81, 0x83, 0x84, 0x85, 0x87, 0x88, 0x89, 0x8b, 0x00, 0x7f}
	fuzz(t, 5000, fuzzCase{
		vars: []string{"cfg", "aux_flags", "hid_report", "hid_state", "power_state", "loop_mask", "play_alt", "rec_alt",
			"play_flags+0", "play_flags+1", "rec_flags+0", "rec_flags+1", "aux_path_flags+0", "aux_path_flags+1",
			"spare_27da", "spare_27e3", "stream+0", "stream+1", "stream+2", "stream+3", "stream+4", "stream+5"},
		setup: func(rng *rand.Rand, m [2]*machine, t *testing.T) string {
			cmd := commands[rng.Intn(len(commands))]
			p1, p2 := byte(rng.Intn(256)), byte(rng.Intn(256))
			switch cmd {
			case 0x32:
				p1 = byte(rng.Intn(4))
			case 0x34:
				p1 = byte(rng.Intn(3))
			case 0x38:
				p1 = overrides[rng.Intn(len(overrides))]
			case 0xa0:
				p1, p2 = byte(rng.Intn(3)), byte(rng.Intn(3))
			case 0xd0:
				p1 = 0x18 + byte(rng.Intn(8))
			case 0xd2:
				p1 = 0x06 + byte(rng.Intn(2))
			}
			var shared [0x9c0]byte
			rng.Read(shared[:])
			for _, x := range m {
				x.chip.Regs[0][0x90], x.chip.Regs[0][0x91], x.chip.Regs[0][0x92] = cmd, p1, p2
				copy(x.chip.RAM[0x1800:], shared[:]) // up to the volume blocks at 0x21c0
			}
			// The original keeps the DAC's I2C address in a variable that
			// start-up has set by the time commands can arrive; the port
			// uses a constant.
			m[0].chip.RAM[0x2777] = 0x90
			return fmt.Sprintf("command %02x %02x %02x", cmd, p1, p2)
		},
		run: func(m *machine, t *testing.T) { m.call(t, "mailbox_service") },
	})
}
