package emu

import (
	"fmt"
	"os"
	"testing"
)

// TestInitialRAM prints what the original start-up code leaves in external
// RAM before main runs (set INIT_DUMP=1 to see it).
func TestInitialRAM(t *testing.T) {
	img := readImage(t, origPath)
	chip := &Chip{Addr: 0x90}
	cpu := &CPU{Bus: chip}
	copy(cpu.Code[:], img)
	const mainAddr = 0x3ada
	for i := 0; cpu.PC != mainAddr; i++ {
		if i > 1_000_000 {
			t.Fatal("never reached main")
		}
		if err := cpu.Step(); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("INIT_DUMP") == "" {
		return
	}
	for a := 0; a < 0x10000; {
		if chip.RAM[a] == 0 {
			a++
			continue
		}
		end := a
		for z := 0; end < 0x10000 && z < 4; end++ {
			if chip.RAM[end] == 0 {
				z++
			} else {
				z = 0
			}
		}
		end -= 4
		fmt.Printf("X:%04x-%04x % x\n", a, end-1, chip.RAM[a:end])
		a = end
	}
	fmt.Printf("bit area 20-2f: % x  sp=%02x\n", cpu.RAM[0x20:0x30], cpu.SFR[sfrSP-0x80])
}
