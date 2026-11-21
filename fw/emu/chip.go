package emu

import "fmt"

// Chip models what the firmware sees in external data space: plain RAM, the
// CT7601's banked registers at 0x4000, and an I2C device behind the bridge's
// I2C master. It records every register and I2C write.
type Chip struct {
	RAM   [0x10000]byte
	Regs  [0x100][0x100]byte // [bank][register]; register 0 is the bank select
	Bank  byte
	Slave [0x100]byte // register file of the I2C device
	Addr  byte        // 8-bit write address the device answers on

	Log []string // register writes outside the I2C bank, and decoded I2C writes
	Raw []string // every register write, bank switches included

	CPU *CPU // when set, writes made inside an interrupt handler are not logged

	Unlocked bool // the DAC reports no lock on its input clocks
	Stuck    bool // the I2C master never finishes a byte

	Nack  int    // this many upcoming I2C bytes are not acknowledged
	OnAck func() // called once, when the handler acknowledges interrupt status

	i2cNacked bool

	i2cArmed            byte // last command written to register 0x14
	i2cPhase            int  // bytes sent since START
	i2cTarget, i2cIndex byte
	i2cSelected         bool
}

const (
	regBase   = 0x4000
	bankI2C   = 0x0e
	i2cCtl    = 0x10
	i2cTx     = 0x12
	i2cRx     = 0x13
	i2cCmd    = 0x14
	ctlStart  = 0xc0
	i2cStatus = 0x01 // done, acknowledged
)

// Record fills in the request record (bank 6, 0xf0 on) as the chip leaves it
// after a SET_INTERFACE (no control selector, no entity) or a class SET_CUR.
func (c *Chip) Record(valueLow, valueHigh, indexLow, entity byte) {
	r := c.Regs[6][0xf0:0xf8]
	r[0], r[1] = 0x21, 0x01
	if valueHigh == 0 && entity == 0 {
		r[0], r[1] = 0x01, 0x0b
	}
	r[2], r[3], r[4], r[5] = valueLow, valueHigh, indexLow, entity
}

func (c *Chip) Read(addr uint16) byte {
	if addr&0xff00 != regBase {
		return c.RAM[addr]
	}
	reg := byte(addr)
	switch {
	case reg == 0:
		return c.Bank
	case c.Bank == bankI2C && reg == i2cCtl:
		if c.Stuck {
			return c.Regs[c.Bank][reg] &^ i2cStatus
		}
		if c.i2cNacked {
			return c.Regs[c.Bank][reg] | i2cStatus | 0x08
		}
		return c.Regs[c.Bank][reg] | i2cStatus
	}
	return c.Regs[c.Bank][reg]
}

func (c *Chip) Write(addr uint16, v byte) {
	if addr&0xff00 != regBase {
		c.RAM[addr] = v
		return
	}
	reg := byte(addr)
	quiet := c.CPU != nil && c.CPU.inISR
	if !quiet {
		c.Raw = append(c.Raw, fmt.Sprintf("%02x:%02x=%02x", c.Bank, reg, v))
	}
	if reg == 0 {
		c.Bank = v
		return
	}
	if c.Bank == 0 && (reg == 0x10 || reg == 0x11) {
		// Interrupt status: assumed write-one-to-clear, which is how the
		// handler uses it.
		c.Regs[0][reg] &^= v
		if reg == 0x10 && c.OnAck != nil {
			f := c.OnAck
			c.OnAck = nil
			f()
		}
	} else {
		c.Regs[c.Bank][reg] = v
	}
	if c.Bank != bankI2C {
		if !quiet {
			c.Log = append(c.Log, fmt.Sprintf("reg %02x:%02x=%02x", c.Bank, reg, v))
		}
		return
	}
	switch reg {
	case i2cCtl:
		if v == ctlStart {
			c.i2cPhase = 0
			c.i2cNacked = false
		}
	case i2cCmd:
		c.i2cArmed = v
		if v == 3 && c.i2cSelected {
			v := c.Slave[c.i2cIndex]
			if c.i2cIndex == 0x40 && !c.Unlocked {
				v |= 0x01 // the ES9018K2M's lock bit
			}
			c.Regs[c.Bank][i2cRx] = v
			c.i2cIndex++
		}
	case i2cTx:
		c.i2cByte(v)
	}
}

func (c *Chip) i2cByte(v byte) {
	if c.Nack > 0 {
		c.Nack--
		c.i2cNacked = true
	}
	if c.i2cNacked { // nothing after an unacknowledged byte counts
		return
	}
	switch c.i2cPhase {
	case 0:
		c.i2cTarget = v
		c.i2cSelected = v&0xfe == c.Addr
	case 1:
		c.i2cIndex = v
	default:
		if c.i2cSelected {
			c.Slave[c.i2cIndex] = v
		}
		c.Log = append(c.Log, fmt.Sprintf("i2c %02x[%02x]=%02x", c.i2cTarget, c.i2cIndex, v))
		c.i2cIndex++
	}
	c.i2cPhase++
}
