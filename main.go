// ct7601-isp talks to a Comtrue CT7601 USB audio bridge over EP0 vendor
// requests, as recovered from the vendor's Windows maintenance tool.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

const usage = `usage: ct7601-isp [-vid 2fc6] [-pid f801] [-v] [-nopause] COMMAND

device
  info                          USB identity and what the bridge is playing
  status [-w]                   stream state as the firmware sees it; -w keeps watching
  reg read [-bank N] ADDR [COUNT]
  reg dump [-bank N]            all 256 registers of a bank
  reg write [-bank N] ADDR VALUE
  i2c scan                      probe the bus behind the bridge (the DAC chip)
  i2c read ADDR REG [COUNT]     ADDR is the 7-bit device address
  i2c write ADDR REG VALUE...

flash (each command enters ISP mode, which stops audio, and resets the chip after)
  flash id                      JEDEC id and status register
  flash read FILE               dump the whole flash
  flash verify FILE             compare the flash with an image
  flash selftest                erase/program/read-back on the empty last sector
  flash write -yes FILE         rewrite the sectors that differ; the old contents are saved first

image (offline, no device needed)
  image info FILE
  image set -o OUT [-like OTHER] [-vid X] [-pid X] [-uac1-pid X] [-manufacturer S]
            [-product S] [-serial S] [-play S] [-record S] [-play2 S]
            [-uac1-product S] FILE
                                -like copies the whole identity of another image first
`

func main() {
	vid := flag.String("vid", "2fc6", "USB vendor id (hex)")
	pid := flag.String("pid", "f801", "USB product id (hex)")
	verbose := flag.Bool("v", false, "log every control transfer")
	noPause := flag.Bool("nopause", false, "do not pause the firmware around I2C access")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	open := func() (*dev, error) {
		d, err := openDev(*vid, *pid, *verbose)
		if err != nil {
			return nil, err
		}
		d.noPause = *noPause
		// Do not leave the firmware paused if the tool is interrupted.
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sig
			d.resumeFirmware()
			os.Exit(130)
		}()
		return d, nil
	}

	var err error
	switch args[0] {
	case "info":
		err = withDev(open, cmdInfo)
	case "status":
		err = cmdStatus(open, args[1:])
	case "reg":
		err = cmdReg(open, args[1:])
	case "i2c":
		err = cmdI2C(open, args[1:])
	case "flash":
		err = cmdFlash(open, args[1:])
	case "image":
		err = cmdImage(args[1:])
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if errors.Is(err, errUsage) {
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

func withDev(open func() (*dev, error), fn func(*dev) error) error {
	d, err := open()
	if err != nil {
		return err
	}
	defer d.close()
	return fn(d)
}

func num(s string, max uint64) (int, error) {
	v, err := strconv.ParseUint(s, 0, 32)
	if err != nil || v > max {
		return 0, fmt.Errorf("bad number %q (max %#x)", s, max)
	}
	return int(v), nil
}

func cmdInfo(d *dev) error {
	fmt.Printf("device        %s:%s rev %s, %s Mb/s\n", sysAttr(d.sysfs, "idVendor"), sysAttr(d.sysfs, "idProduct"), sysAttr(d.sysfs, "bcdDevice"), sysAttr(d.sysfs, "speed"))
	fmt.Printf("manufacturer  %s\n", sysAttr(d.sysfs, "manufacturer"))
	fmt.Printf("product       %s\n", sysAttr(d.sysfs, "product"))
	fmt.Printf("serial        %s\n", sysAttr(d.sysfs, "serial"))
	s, err := d.streamStatus()
	if err != nil {
		return err
	}
	fmt.Printf("stream        %s\n", s)
	return nil
}

func cmdStatus(open func() (*dev, error), args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	watch := fs.Bool("w", false, "")
	if fs.Parse(args) != nil {
		return errUsage
	}
	return withDev(open, func(d *dev) error {
		var last streamStatus
		for first := true; ; first = false {
			s, err := d.streamStatus()
			if err != nil {
				return err
			}
			if first || s != last {
				if *watch {
					fmt.Print(time.Now().Format("15:04:05.000  "))
				}
				fmt.Println(s)
				last = s
			}
			if !*watch {
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

func cmdReg(open func() (*dev, error), args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	fs := flag.NewFlagSet("reg", flag.ContinueOnError)
	bank := fs.Uint("bank", bankMain, "")
	if fs.Parse(args[1:]) != nil || *bank > 0xff {
		return errUsage
	}
	rest := fs.Args()
	return withDev(open, func(d *dev) error {
		return d.withBank(byte(*bank), func() error {
			switch {
			case args[0] == "dump" && len(rest) == 0:
				return regDump(d, 0, 0x100)
			case args[0] == "read" && (len(rest) == 1 || len(rest) == 2):
				addr, err := num(rest[0], 0xff)
				if err != nil {
					return err
				}
				count := 1
				if len(rest) == 2 {
					if count, err = num(rest[1], 0x100); err != nil {
						return err
					}
				}
				return regDump(d, addr, min(count, 0x100-addr))
			case args[0] == "write" && len(rest) == 2:
				addr, err := num(rest[0], 0xff)
				if err != nil {
					return err
				}
				val, err := num(rest[1], 0xff)
				if err != nil {
					return err
				}
				return d.regWrite(uint16(addr), byte(val))
			}
			return errUsage
		})
	})
}

func regDump(d *dev, addr, count int) error {
	buf := make([]byte, count)
	for i := range buf {
		v, err := d.regRead(uint16(addr + i))
		if err != nil {
			return err
		}
		buf[i] = v
	}
	hexdump(addr, buf)
	return nil
}

func hexdump(base int, buf []byte) {
	for off := 0; off < len(buf); off += 16 {
		fmt.Printf("%02x: % x\n", base+off, buf[off:min(off+16, len(buf))])
	}
}

func cmdI2C(open func() (*dev, error), args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	return withDev(open, func(d *dev) error {
		switch {
		case args[0] == "scan" && len(args) == 1:
			for a := 0x08; a < 0x78; a++ {
				ok, err := d.i2cProbe(byte(a))
				if err != nil {
					return err
				}
				if ok {
					fmt.Printf("device at %#02x\n", a)
				}
			}
			return nil
		case args[0] == "read" && (len(args) == 3 || len(args) == 4):
			addr, err := num(args[1], 0x7f)
			if err != nil {
				return err
			}
			reg, err := num(args[2], 0xff)
			if err != nil {
				return err
			}
			count := 1
			if len(args) == 4 {
				if count, err = num(args[3], 0x100); err != nil {
					return err
				}
			}
			// One register per transaction, the way the vendor tool reads.
			buf := make([]byte, min(count, 0x100-reg))
			for i := range buf {
				b, err := d.i2cReadReg(byte(addr), byte(reg+i), 1)
				if err != nil {
					return fmt.Errorf("reg %#x: %w", reg+i, err)
				}
				buf[i] = b[0]
			}
			hexdump(reg, buf)
			return nil
		case args[0] == "write" && len(args) >= 4:
			addr, err := num(args[1], 0x7f)
			if err != nil {
				return err
			}
			reg, err := num(args[2], 0xff)
			if err != nil {
				return err
			}
			var data []byte
			for _, a := range args[3:] {
				v, err := num(a, 0xff)
				if err != nil {
					return err
				}
				data = append(data, byte(v))
			}
			return d.i2cWriteReg(byte(addr), byte(reg), data)
		}
		return errUsage
	})
}

// inISP runs fn in ISP mode and always resets the chip back to firmware.
func inISP(open func() (*dev, error), fn func(*dev) error) error {
	return withDev(open, func(d *dev) error {
		if err := d.enterISP(); err != nil {
			return err
		}
		err := fn(d)
		if e := d.exitISP(); err == nil {
			err = e
		}
		return err
	})
}

func progress(total int) func(int) {
	return func(done int) {
		if done%0x4000 == 0 || done == total {
			fmt.Fprintf(os.Stderr, "\r%3d%%", done*100/total)
			if done == total {
				fmt.Fprintln(os.Stderr)
			}
		}
	}
}

func readAll(d *dev) ([]byte, error) {
	id, err := d.flashID()
	if err != nil {
		return nil, err
	}
	size, err := id.size()
	if err != nil {
		return nil, err
	}
	return d.flashRead(0, size, progress(size))
}

func cmdFlash(open func() (*dev, error), args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	switch {
	case args[0] == "id" && len(args) == 1:
		return inISP(open, func(d *dev) error {
			id, err := d.flashID()
			if err != nil {
				return err
			}
			sr, err := d.flashStatus()
			if err != nil {
				return err
			}
			size, _ := id.size()
			prot := "not write-protected"
			if sr&statusProtect != 0 {
				prot = "write-protected"
			}
			fmt.Printf("jedec id % x, %d bytes, status %02x (%s)\n", id[:], size, sr, prot)
			return nil
		})
	case args[0] == "selftest" && len(args) == 1:
		err := inISP(open, func(d *dev) error {
			return d.flashSelfTest(writeOpts{log: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }})
		})
		if err == nil {
			fmt.Println("erase, program and read-back work")
		}
		return err
	case args[0] == "read" && len(args) == 2:
		return inISP(open, func(d *dev) error {
			img, err := readAll(d)
			if err != nil {
				return err
			}
			return os.WriteFile(args[1], img, 0o644)
		})
	case args[0] == "verify" && len(args) == 2:
		want, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		return inISP(open, func(d *dev) error {
			got, err := readAll(d)
			if err != nil {
				return err
			}
			if !bytes.Equal(trimImage(got), trimImage(want)) {
				return errors.New("flash differs from " + args[1])
			}
			fmt.Println("flash matches", args[1])
			return nil
		})
	case args[0] == "write":
		fs := flag.NewFlagSet("flash write", flag.ContinueOnError)
		yes := fs.Bool("yes", false, "")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 1 {
			return errUsage
		}
		img, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return err
		}
		img = trimImage(img)
		if _, err := findTable(img); err != nil {
			return err
		}
		if !*yes {
			return errors.New("flash write erases the firmware; pass -yes to go ahead")
		}
		backup := "ct7601-backup-" + time.Now().Format("20060102-150405") + ".bin"
		err = withDev(open, func(d *dev) error {
			if err := d.enterISP(); err != nil {
				return err
			}
			invalid, err := d.flashWrite(img, writeOpts{backup: backup, log: func(f string, a ...any) {
				fmt.Fprintf(os.Stderr, f+"\n", a...)
			}})
			if invalid {
				// No reset: the chip stays in ISP mode with the firmware stopped.
				return fmt.Errorf("%w\nThe flash holds no working firmware and the chip has NOT been reset.\nKeep it plugged in and run this command again, or write %s back.\nUnplugging it now may leave it unable to start", err, backup)
			}
			if e := d.exitISP(); err == nil {
				err = e
			}
			return err
		})
		if err != nil {
			return err
		}
		fmt.Println("done, chip reset")
		return nil
	}
	return errUsage
}

func cmdImage(args []string) error {
	if len(args) < 2 {
		return errUsage
	}
	switch args[0] {
	case "info":
		img, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		s, err := imageInfo(img)
		fmt.Print(s)
		return err
	case "set":
		fs := flag.NewFlagSet("image set", flag.ContinueOnError)
		out := fs.String("o", "", "")
		like := fs.String("like", "", "")
		vals := map[string]*string{"vid": fs.String("vid", "", ""), "pid": fs.String("pid", "", ""), "uac1-pid": fs.String("uac1-pid", "", "")}
		for _, s := range tableStrings {
			vals[s.name] = fs.String(s.name, "", "")
		}
		if fs.Parse(args[1:]) != nil || fs.NArg() != 1 || *out == "" {
			return errUsage
		}
		fields := map[string]string{}
		fs.Visit(func(f *flag.Flag) {
			if f.Name != "o" && f.Name != "like" {
				fields[f.Name] = *vals[f.Name]
			}
		})
		img, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return err
		}
		img = trimImage(img)
		if *like != "" {
			other, err := os.ReadFile(*like)
			if err != nil {
				return err
			}
			if img, err = imageLike(img, other); err != nil {
				return err
			}
		}
		patched, err := imageSet(img, fields)
		if err != nil {
			return err
		}
		return os.WriteFile(*out, patched, 0o644)
	}
	return errUsage
}
