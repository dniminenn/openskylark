// Package emu is a small 8051 core used to run the firmware off-device and
// record what it does to the CT7601's registers.
package emu

import "fmt"

const (
	sfrSP   = 0x81
	sfrDPL  = 0x82
	sfrDPH  = 0x83
	sfrTCON = 0x88
	sfrTL0  = 0x8a
	sfrTH0  = 0x8c
	sfrSCON = 0x98
	sfrSBUF = 0x99
	sfrP2   = 0xa0
	sfrIE   = 0xa8
	sfrPSW  = 0xd0
	sfrACC  = 0xe0
	sfrB    = 0xf0
)

// Bus is the external data space: RAM plus the chip's memory-mapped registers.
type Bus interface {
	Read(addr uint16) byte
	Write(addr uint16, v byte)
}

type CPU struct {
	Code  [0x10000]byte
	RAM   [0x100]byte // internal data, 0x80-0xff reachable only indirectly
	SFR   [0x80]byte
	PC    uint16
	Bus   Bus
	Steps uint64
	UART  []byte // everything the firmware has written to the serial port
	IRQ0  bool   // external interrupt 0 is asserted; taken once enabled
	Paged int    // MOVX @Ri executed: these depend on P2 paging, which the chip may not have
	NoTI  bool   // the serial port never raises its transmit-done flag
	inISR bool
}

func (c *CPU) a() byte     { return c.SFR[sfrACC-0x80] }
func (c *CPU) setA(v byte) { c.SFR[sfrACC-0x80] = v }
func (c *CPU) psw() byte   { return c.SFR[sfrPSW-0x80] }
func (c *CPU) carry() byte { return c.psw() >> 7 }
func (c *CPU) dptr() uint16 {
	return uint16(c.SFR[sfrDPH-0x80])<<8 | uint16(c.SFR[sfrDPL-0x80])
}
func (c *CPU) setDPTR(v uint16) {
	c.SFR[sfrDPH-0x80], c.SFR[sfrDPL-0x80] = byte(v>>8), byte(v)
}
func (c *CPU) setFlag(mask byte, on bool) {
	if on {
		c.SFR[sfrPSW-0x80] |= mask
	} else {
		c.SFR[sfrPSW-0x80] &^= mask
	}
}
func (c *CPU) setCarry(on bool) { c.setFlag(0x80, on) }

// R reads and SetR writes register n of the active bank.
func (c *CPU) R(n byte) byte       { return c.RAM[c.psw()&0x18|n] }
func (c *CPU) SetR(n byte, v byte) { c.RAM[c.psw()&0x18|n] = v }
func (c *CPU) direct(addr byte) byte {
	if addr < 0x80 {
		return c.RAM[addr]
	}
	if addr == sfrPSW { // parity of A is live
		p := c.a()
		p ^= p >> 4
		p ^= p >> 2
		p ^= p >> 1
		return c.psw()&0xfe | p&1
	}
	return c.SFR[addr-0x80]
}
func (c *CPU) setDirect(addr, v byte) {
	switch {
	case addr < 0x80:
		c.RAM[addr] = v
	case addr == sfrSBUF:
		// Sent at once. The transmit-done flag is raised only in the one
		// set-up it has been seen to work in on a real CT7601: receiver
		// enabled and serial interrupt unmasked. With both off it never
		// came up.
		c.UART = append(c.UART, v)
		if !c.NoTI && c.SFR[sfrSCON-0x80]&0x10 != 0 && c.SFR[sfrIE-0x80]&0x10 != 0 {
			c.SFR[sfrSCON-0x80] |= 0x02
		}
	default:
		c.SFR[addr-0x80] = v
	}
}
func bitAddr(b byte) (addr, mask byte) {
	if b < 0x80 {
		return 0x20 + b>>3, 1 << (b & 7)
	}
	return b & 0xf8, 1 << (b & 7)
}
func (c *CPU) bit(b byte) bool {
	addr, mask := bitAddr(b)
	return c.direct(addr)&mask != 0
}
func (c *CPU) setBit(b byte, on bool) {
	addr, mask := bitAddr(b)
	v := c.direct(addr) &^ mask
	if on {
		v |= mask
	}
	c.setDirect(addr, v)
}
func (c *CPU) push(v byte) {
	c.SFR[sfrSP-0x80]++
	c.RAM[c.SFR[sfrSP-0x80]] = v
}
func (c *CPU) pop() byte {
	v := c.RAM[c.SFR[sfrSP-0x80]]
	c.SFR[sfrSP-0x80]--
	return v
}
func (c *CPU) fetch() byte {
	v := c.Code[c.PC]
	c.PC++
	return v
}
func (c *CPU) fetch16() uint16 {
	hi := c.fetch()
	return uint16(hi)<<8 | uint16(c.fetch())
}
func (c *CPU) rel() {
	off := int8(c.fetch())
	c.PC += uint16(int16(off))
}
func (c *CPU) branch(cond bool) {
	off := int8(c.fetch())
	if cond {
		c.PC += uint16(int16(off))
	}
}

func (c *CPU) add(v, carry byte) {
	a := c.a()
	sum := uint16(a) + uint16(v) + uint16(carry)
	c.setCarry(sum > 0xff)
	c.setFlag(0x40, a&0x0f+v&0x0f+carry > 0x0f)
	c7 := (a&0x7f + v&0x7f + carry) >> 7
	c.setFlag(0x04, c7 != byte(sum>>8))
	c.setA(byte(sum))
}
func (c *CPU) subb(v byte) {
	a, carry := c.a(), c.carry()
	diff := int16(a) - int16(v) - int16(carry)
	b6 := int16(a&0x7f) - int16(v&0x7f) - int16(carry)
	c.setCarry(diff < 0)
	c.setFlag(0x40, int16(a&0x0f)-int16(v&0x0f)-int16(carry) < 0)
	c.setFlag(0x04, (b6 < 0) != (diff < 0))
	c.setA(byte(diff))
}

// operand resolves the low-nibble addressing modes 5 (direct), 6-7 (@Ri) and
// 8-f (Rn). The direct address byte is fetched here.
type operand struct {
	c    *CPU
	mode byte
	addr byte
}

func (c *CPU) operand(op byte) operand {
	o := operand{c: c, mode: op & 0x0f}
	switch {
	case o.mode == 5:
		o.addr = c.fetch()
	case o.mode == 6 || o.mode == 7:
		o.addr = c.R(o.mode - 6)
	}
	return o
}
func (o operand) get() byte {
	switch {
	case o.mode == 5:
		return o.c.direct(o.addr)
	case o.mode < 8:
		return o.c.RAM[o.addr]
	}
	return o.c.R(o.mode - 8)
}
func (o operand) set(v byte) {
	switch {
	case o.mode == 5:
		o.c.setDirect(o.addr, v)
	case o.mode < 8:
		o.c.RAM[o.addr] = v
	default:
		o.c.SetR(o.mode-8, v)
	}
}

// Interrupt enters the handler at vector unless one is already running.
func (c *CPU) Interrupt(vector uint16) bool {
	if c.inISR {
		return false
	}
	c.inISR = true
	c.push(byte(c.PC))
	c.push(byte(c.PC >> 8))
	c.PC = vector
	return true
}

// tick advances timer 0 (mode 1) by one count per instruction and raises its
// interrupt on overflow. Real timing is not modelled.
func (c *CPU) tick() {
	if c.SFR[sfrTCON-0x80]&0x10 == 0 {
		return
	}
	c.SFR[sfrTL0-0x80]++
	if c.SFR[sfrTL0-0x80] != 0 {
		return
	}
	c.SFR[sfrTH0-0x80]++
	if c.SFR[sfrTH0-0x80] == 0 {
		c.SFR[sfrTCON-0x80] |= 0x20
	}
}

func (c *CPU) pendingTimer0() {
	if c.SFR[sfrTCON-0x80]&0x20 != 0 && c.SFR[sfrIE-0x80]&0x82 == 0x82 && c.Interrupt(0x000b) {
		c.SFR[sfrTCON-0x80] &^= 0x20
	}
}

// Step runs one instruction.
func (c *CPU) Step() error {
	if c.IRQ0 && c.SFR[sfrIE-0x80]&0x81 == 0x81 && c.Interrupt(0x0003) {
		c.IRQ0 = false
	}
	// Serial: RI or TI set with the serial interrupt unmasked.
	if c.SFR[sfrSCON-0x80]&0x03 != 0 && c.SFR[sfrIE-0x80]&0x90 == 0x90 {
		c.Interrupt(0x0023)
	}
	c.pendingTimer0()
	at := c.PC
	op := c.fetch()
	c.Steps++
	c.tick()
	low, high := op&0x0f, op&0xf0

	switch {
	case low == 1: // ajmp, acall
		target := (c.PC+1)&0xf800 | uint16(op>>5)<<8 | uint16(c.fetch())
		if op&0x10 != 0 {
			c.push(byte(c.PC))
			c.push(byte(c.PC >> 8))
		}
		c.PC = target
		return nil
	case low >= 5 && high != 0x70 && high != 0x80 && high != 0xa0 && high != 0xb0 && high != 0xd0 && op != 0x85 && op != 0xa5 && op != 0xf5 || op == 0xd6 || op == 0xd7:
		// The regular column operations on direct, @Ri and Rn.
		if op == 0xd5 {
			break
		}
		o := c.operand(op)
		v := o.get()
		switch high {
		case 0x00:
			o.set(v + 1)
		case 0x10:
			o.set(v - 1)
		case 0x20:
			c.add(v, 0)
		case 0x30:
			c.add(v, c.carry())
		case 0x40:
			c.setA(c.a() | v)
		case 0x50:
			c.setA(c.a() & v)
		case 0x60:
			c.setA(c.a() ^ v)
		case 0x90:
			c.subb(v)
		case 0xc0:
			o.set(c.a())
			c.setA(v)
		case 0xd0: // xchd a,@Ri
			o.set(v&0xf0 | c.a()&0x0f)
			c.setA(c.a()&0xf0 | v&0x0f)
		case 0xe0:
			c.setA(v)
		case 0xf0:
			o.set(c.a())
		}
		return nil
	}

	switch {
	case low >= 6 && high == 0x70: // mov @Ri/Rn,#imm
		o := c.operand(op)
		o.set(c.fetch())
	case low >= 6 && high == 0x80: // mov direct,@Ri/Rn
		o := c.operand(op)
		c.setDirect(c.fetch(), o.get())
	case low >= 6 && high == 0xa0: // mov @Ri/Rn,direct
		o := c.operand(op)
		o.set(c.direct(c.fetch()))
	case low >= 6 && high == 0xb0: // cjne @Ri/Rn,#imm,rel
		v, imm := c.operand(op).get(), c.fetch()
		c.setCarry(v < imm)
		c.branch(v != imm)
	case low >= 8 && high == 0xd0: // djnz Rn,rel
		o := c.operand(op)
		v := o.get() - 1
		o.set(v)
		c.branch(v != 0)
	default:
		switch op {
		case 0x00:
		case 0x02:
			c.PC = c.fetch16()
		case 0x12:
			target := c.fetch16()
			c.push(byte(c.PC))
			c.push(byte(c.PC >> 8))
			c.PC = target
		case 0x22, 0x32:
			hi := c.pop()
			c.PC = uint16(hi)<<8 | uint16(c.pop())
			if op == 0x32 {
				c.inISR = false
			}
		case 0x03:
			c.setA(c.a()>>1 | c.a()<<7)
		case 0x13:
			a := c.a()
			c.setA(a>>1 | c.carry()<<7)
			c.setCarry(a&1 != 0)
		case 0x23:
			c.setA(c.a()<<1 | c.a()>>7)
		case 0x33:
			a := c.a()
			c.setA(a<<1 | c.carry())
			c.setCarry(a&0x80 != 0)
		case 0x04:
			c.setA(c.a() + 1)
		case 0x14:
			c.setA(c.a() - 1)
		case 0x24:
			c.add(c.fetch(), 0)
		case 0x34:
			c.add(c.fetch(), c.carry())
		case 0x44:
			c.setA(c.a() | c.fetch())
		case 0x54:
			c.setA(c.a() & c.fetch())
		case 0x64:
			c.setA(c.a() ^ c.fetch())
		case 0x74:
			c.setA(c.fetch())
		case 0x94:
			c.subb(c.fetch())
		case 0x84:
			a, b := c.a(), c.SFR[sfrB-0x80]
			c.setCarry(false)
			c.setFlag(0x04, b == 0)
			if b != 0 {
				c.setA(a / b)
				c.SFR[sfrB-0x80] = a % b
			}
		case 0xa4:
			p := uint16(c.a()) * uint16(c.SFR[sfrB-0x80])
			c.setA(byte(p))
			c.SFR[sfrB-0x80] = byte(p >> 8)
			c.setCarry(false)
			c.setFlag(0x04, p > 0xff)
		case 0xb4:
			imm := c.fetch()
			c.setCarry(c.a() < imm)
			c.branch(c.a() != imm)
		case 0xb5:
			v := c.direct(c.fetch())
			c.setCarry(c.a() < v)
			c.branch(c.a() != v)
		case 0xc4:
			c.setA(c.a()<<4 | c.a()>>4)
		case 0xd4:
			a := uint16(c.a())
			if a&0x0f > 9 || c.psw()&0x40 != 0 {
				a += 6
			}
			if a > 0x9f || c.carry() != 0 || a > 0xff {
				a += 0x60
			}
			if a > 0xff {
				c.setCarry(true)
			}
			c.setA(byte(a))
		case 0xe4:
			c.setA(0)
		case 0xf4:
			c.setA(^c.a())
		case 0x10:
			b := c.fetch()
			set := c.bit(b)
			if set {
				c.setBit(b, false)
			}
			c.branch(set)
		case 0x20:
			c.branch(c.bit(c.fetch()))
		case 0x30:
			c.branch(!c.bit(c.fetch()))
		case 0x40:
			c.branch(c.carry() != 0)
		case 0x50:
			c.branch(c.carry() == 0)
		case 0x60:
			c.branch(c.a() == 0)
		case 0x70:
			c.branch(c.a() != 0)
		case 0x80:
			c.rel()
		case 0x90:
			c.setDPTR(c.fetch16())
		case 0xa0:
			c.setCarry(c.carry() != 0 || !c.bit(c.fetch()))
		case 0xb0:
			c.setCarry(c.carry() != 0 && !c.bit(c.fetch()))
		case 0x72:
			c.setCarry(c.carry() != 0 || c.bit(c.fetch()))
		case 0x82:
			c.setCarry(c.carry() != 0 && c.bit(c.fetch()))
		case 0x92:
			c.setBit(c.fetch(), c.carry() != 0)
		case 0xa2:
			c.setCarry(c.bit(c.fetch()))
		case 0xb2:
			b := c.fetch()
			c.setBit(b, !c.bit(b))
		case 0xc2:
			c.setBit(c.fetch(), false)
		case 0xd2:
			c.setBit(c.fetch(), true)
		case 0xb3:
			c.setCarry(c.carry() == 0)
		case 0xc3:
			c.setCarry(false)
		case 0xd3:
			c.setCarry(true)
		case 0xc0:
			c.push(c.direct(c.fetch()))
		case 0xd0:
			c.setDirect(c.fetch(), c.pop())
		case 0x42, 0x52, 0x62:
			addr := c.fetch()
			c.setDirect(addr, logic(op, c.direct(addr), c.a()))
		case 0x43, 0x53, 0x63:
			addr := c.fetch()
			c.setDirect(addr, logic(op, c.direct(addr), c.fetch()))
		case 0x75:
			addr := c.fetch()
			c.setDirect(addr, c.fetch())
		case 0x85:
			src := c.fetch()
			c.setDirect(c.fetch(), c.direct(src))
		case 0xf5:
			c.setDirect(c.fetch(), c.a())
		case 0xd5:
			addr := c.fetch()
			v := c.direct(addr) - 1
			c.setDirect(addr, v)
			c.branch(v != 0)
		case 0x73:
			c.PC = c.dptr() + uint16(c.a())
		case 0x83:
			c.setA(c.Code[c.PC+uint16(c.a())])
		case 0x93:
			c.setA(c.Code[c.dptr()+uint16(c.a())])
		case 0xa3:
			c.setDPTR(c.dptr() + 1)
		case 0xe0:
			c.setA(c.Bus.Read(c.dptr()))
		case 0xf0:
			c.Bus.Write(c.dptr(), c.a())
		case 0xe2, 0xe3:
			c.Paged++
			c.setA(c.Bus.Read(uint16(c.SFR[sfrP2-0x80])<<8 | uint16(c.R(op&1))))
		case 0xf2, 0xf3:
			c.Paged++
			c.Bus.Write(uint16(c.SFR[sfrP2-0x80])<<8|uint16(c.R(op&1)), c.a())
		default:
			return fmt.Errorf("undefined opcode %02x at %04x", op, at)
		}
	}
	return nil
}

func logic(op, x, y byte) byte {
	switch op & 0xf0 {
	case 0x40:
		return x | y
	case 0x50:
		return x & y
	}
	return x ^ y
}

// Call runs the subroutine at addr until it returns, or fails after limit
// instructions.
func (c *CPU) Call(addr uint16, limit uint64) error {
	const sentinel = 0xfff0
	c.push(byte(sentinel & 0xff))
	c.push(byte(sentinel >> 8))
	c.PC = addr
	for end := c.Steps + limit; c.PC != sentinel; {
		if c.Steps >= end {
			return fmt.Errorf("call %04x: still running at %04x after %d instructions", addr, c.PC, limit)
		}
		if err := c.Step(); err != nil {
			return err
		}
	}
	return nil
}
