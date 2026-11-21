package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Every bridge request is a vendor control transfer with recipient "other".
const (
	reqOut = 0x43
	reqIn  = 0xc3
)

type ctrlTransfer struct {
	reqType uint8
	req     uint8
	value   uint16
	index   uint16
	length  uint16
	timeout uint32
	data    unsafe.Pointer
}

// USBDEVFS_CONTROL = _IOWR('U', 0, struct usbdevfs_ctrltransfer) on 64-bit.
const usbdevfsControl = 0xc0185500

type dev struct {
	f       *os.File
	sysfs   string
	verbose bool

	// transfer does one control transfer; tests replace it.
	transfer func(reqType, req uint8, value, index uint16, buf []byte) (int, error)
	// noPause skips the firmware pause around I2C access.
	noPause bool
	// paused is set while this tool holds the firmware's event handling off.
	paused bool
}

func openDev(vid, pid string, verbose bool) (*dev, error) {
	matches, _ := filepath.Glob("/sys/bus/usb/devices/*/idVendor")
	for _, m := range matches {
		dir := filepath.Dir(m)
		if sysAttr(dir, "idVendor") != vid || sysAttr(dir, "idProduct") != pid {
			continue
		}
		bus, _ := strconv.Atoi(sysAttr(dir, "busnum"))
		num, _ := strconv.Atoi(sysAttr(dir, "devnum"))
		f, err := os.OpenFile(fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, num), os.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		if err := lockExclusive(f); err != nil {
			f.Close()
			return nil, err
		}
		d := &dev{f: f, sysfs: dir, verbose: verbose}
		d.transfer = d.usbfsControl
		return d, nil
	}
	return nil, fmt.Errorf("no USB device %s:%s", vid, pid)
}

func sysAttr(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return strings.TrimSpace(string(b))
}

// lockExclusive keeps a second copy of this tool off the device. The chip has
// one bank select for the host; two programs selecting banks in turn would
// read and write each other's registers. The lock goes when the file closes.
func lockExclusive(f *os.File) error {
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK {
			return fmt.Errorf("lock %s: %w", f.Name(), err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s is in use by another ct7601-isp", f.Name())
		}
	}
}

func (d *dev) close() {
	d.resumeFirmware()
	if d.f != nil {
		d.f.Close()
	}
}

func (d *dev) control(reqType, req uint8, value, index uint16, buf []byte) (int, error) {
	return d.transfer(reqType, req, value, index, buf)
}

func (d *dev) usbfsControl(reqType, req uint8, value, index uint16, buf []byte) (int, error) {
	c := ctrlTransfer{reqType: reqType, req: req, value: value, index: index, length: uint16(len(buf)), timeout: 1000}
	if len(buf) > 0 {
		c.data = unsafe.Pointer(&buf[0])
	}
	n, _, errno := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), usbdevfsControl, uintptr(unsafe.Pointer(&c)))
	if d.verbose {
		shown := buf
		if len(shown) > 16 {
			shown = shown[:16]
		}
		fmt.Fprintf(os.Stderr, "ctl %02x %02x v=%04x i=%04x len=%d -> %d %v % x\n", reqType, req, value, index, len(buf), int(n), errno, shown)
	}
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}
