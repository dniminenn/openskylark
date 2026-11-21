package emu

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// After any sequence of host requests, once a well-behaved stream is flowing
// and the host is quiet, the firmware must end up playing PCM, unmuted, and
// doing nothing more. The vendor image does not always: it can lose the
// final format change and stay in DSD mode.
func TestSettlesAfterAnySequence(t *testing.T) {
	rates := []uint32{44100, 48000, 88200, 96000, 176400, 192000, 352800, 384000}
	for _, v := range variants(t) {
		bad := 0
		for trial := 0; trial < 120 && (bad < 4 || !v.robust); trial++ {
			rng := rand.New(rand.NewSource(int64(trial)))
			cpu, chip, _ := bootPrompt(t, v.path, 2_000_000)
			chip.Regs[7][0x1d] = 0xd8
			var ops []string
			irq := func(s0 byte, a, b, c, d byte) {
				chip.Regs[0][0x10] |= s0
				chip.Record(a, b, c, d)
				cpu.IRQ0 = true
			}
			setRate := func(hz uint32, tell bool) {
				copy(chip.RAM[0x2000:], []byte{byte(hz), byte(hz >> 8), byte(hz >> 16), byte(hz >> 24)})
				if tell {
					irq(0x02, 0, 1, 0, 0x05)
				}
			}
			for n := 0; n < 14; n++ {
				switch op := rng.Intn(11); op {
				case 0, 1:
					hz := rates[rng.Intn(len(rates))]
					tell := rng.Intn(5) != 0
					setRate(hz, tell)
					ops = append(ops, fmt.Sprintf("rate %d tell=%v", hz, tell))
				case 2, 3:
					alt := byte(1 + rng.Intn(4))
					irq(0x04, alt, 0, 1, 0)
					ops = append(ops, fmt.Sprintf("alt %d", alt))
				case 4:
					irq(0x04, 0, 0, 1, 0)
					ops = append(ops, "alt 0")
				case 5:
					chip.Regs[3][0x18] |= 0x08
					ops = append(ops, "data on")
				case 6:
					chip.Regs[3][0x18] &^= 0x08
					ops = append(ops, "data off")
				case 7:
					chip.RAM[0x21c5] = byte(0xe0 + rng.Intn(0x20))
					if rng.Intn(4) != 0 {
						irq(0x02, 0, 2, 0, 0x03)
					}
					ops = append(ops, "volume")
				case 8:
					chip.RAM[0x21c0] = byte(rng.Intn(2))
					if rng.Intn(4) != 0 {
						irq(0x02, 0, 1, 0, 0x03)
					}
					ops = append(ops, fmt.Sprintf("mute %d", chip.RAM[0x21c0]))
				case 9:
					irq(0x40, 0, 0, 0, 0)
					ops = append(ops, "bus reset")
				case 10:
					// the open sequence Linux sends: rate, then the setting 0.8 ms later
					hz := rates[rng.Intn(len(rates))]
					setRate(hz, true)
					run(t, cpu, 800)
					alt := byte(1 + rng.Intn(3))
					irq(0x04, alt, 0, 1, 0)
					chip.Regs[3][0x18] |= 0x08
					ops = append(ops, fmt.Sprintf("open %d alt %d", hz, alt))
				}
				gap := []int{300, 3_000, 30_000, 150_000, 400_000}[rng.Intn(5)]
				run(t, cpu, gap)
				ops = append(ops, fmt.Sprintf("+%dms", gap/1000))
			}
			// Now a well-behaved stream: PCM setting, data, host unmuted.
			setRate(48000, true)
			run(t, cpu, 800)
			irq(0x04, 3, 0, 1, 0)
			chip.Regs[3][0x18] |= 0x08
			chip.RAM[0x21c0] = 0
			run(t, cpu, 5_000)
			irq(0x02, 0, 1, 0, 0x03)
			run(t, cpu, 2_500_000)
			mark := len(chip.Raw)
			run(t, cpu, 1_000_000)
			busy := 0
			for _, w := range chip.Raw[mark:] {
				if strings.HasPrefix(w, "00:a") || strings.HasPrefix(w, "00:00=") || len(w) > 6 && w[2] == ':' && w[3:5] == "00" {
					continue
				}
				busy++
			}
			state := chip.RAM[map[bool]uint16{true: v.sym["stream"], false: 0x27e4}[v.robust]]
			ok := state == 0x15 && chip.Slave[7]&3 == 0 && chip.Regs[2][0x09]&0x02 == 0 && busy == 0
			if !ok {
				bad++
				if v.robust {
					t.Errorf("trial %d: state %#02x, DAC reg 7 %#02x, gpio %#02x, %d writes in the last second\n  %s\n  last writes: %s", trial, state, chip.Slave[7], chip.Regs[2][0x09], busy, strings.Join(ops, ", "), strings.Join(chip.Raw[max(mark, len(chip.Raw)-12):], " "))
				}
			}
		}
		t.Logf("%-8s did not settle in %d of 120 sequences", v.name, bad)
	}
}
