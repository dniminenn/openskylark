/* Host command channel. The host writes parameters to bank 0 registers 0x91
 * and 0x92 and a command to 0x90 (over USB, see the ct7601-isp tool). The
 * firmware sets bit 0 of the command to acknowledge it and answers in
 * 0xa0-0xa3. These are the vendor's debug and production-test commands. */
#include "ct7601.h"
#include "events.h"

#define HOST_COMMAND 0x90
#define HOST_PARAM_1 0x91
#define HOST_PARAM_2 0x92
#define ANSWER(n)    ((uint8_t)(0xa0u + (n))) /* 0xa0-0xa3 */
#define HEARTBEAT    0xad

#define TEST_DEVICE 0x26 /* second I2C address the I2C commands also touch; nothing answers there on this board */

extern __xdata uint8_t hid_state, hid_report, power_state, loop_mask, rec_alt;
__xdata uint8_t aux_path_flags[2]; /* like play_flags, for a path this board lacks */
__xdata uint8_t spare_27da, spare_27e3; /* read back by commands; never written in the code ported so far */

static __bit address(uint8_t addr8)
{
	uint8_t tries;

	for (tries = 0; tries < 10; tries++) {
		i2c_start();
		if (i2c_send(addr8))
			return 1;
		delay(1);
	}
	return 0;
}

static void i2c_write_buf(uint8_t addr8, uint8_t reg, uint8_t len, const uint8_t *data)
{
	uint8_t bank = reg_read(REG_BANK);
	uint8_t i;

	reg_write(REG_BANK, BANK_I2C);
	if (address(addr8)) {
		i2c_send(reg);
		for (i = 0; i < len; i++)
			i2c_send(data[i]);
	}
	i2c_stop();
	reg_write(REG_BANK, bank);
}

static void i2c_read_buf(uint8_t addr8, uint8_t reg, uint8_t len, uint8_t *data)
{
	uint8_t bank = reg_read(REG_BANK);
	uint8_t i;

	reg_write(REG_BANK, BANK_I2C);
	if (address(addr8)) {
		i2c_send(reg);
		if (address(addr8 | 1))
			for (i = 0; i < len; i++)
				data[i] = i2c_recv(i == len - 1);
	}
	i2c_stop();
	reg_write(REG_BANK, bank);
}

/* Command 0x32: clock and PHY power, mode 1 down and mode 2 up. */
static void power_mode(uint8_t mode)
{
	uint8_t bank;

	if (mode == 1) {
		usb_mode((cfg & CFG_HIGH_SPEED) ? 1 : 0);
		bank = REG[REG_BANK];
		reg_write(REG_BANK, 0x01);
		reg_update(0x01, 0x00, 0x07);
		reg_write(REG_BANK, BANK_MAIN);
		reg_write(0x01, 0xa4);
		delay(50);
		reg_write(REG_BANK, 0x02);
		reg_update(0x18, 0x00, 0x40);
		reg_update(0x18, 0x80, 0x80);
		REG[REG_BANK] = bank;
	} else if (mode == 2) {
		usb_mode((cfg & CFG_HIGH_SPEED) ? 3 : 2);
		bank = REG[REG_BANK];
		reg_write(REG_BANK, 0x01);
		reg_update(0x01, 0x02, 0x07);
		reg_write(REG_BANK, BANK_MAIN);
		reg_write(0x01, 0x20);
		REG[REG_BANK] = bank;
	}
}

/* Command 0x38: force format and routing flags. */
static void override(uint8_t what)
{
	uint8_t n = what & 0x0f;

	if (what == 0xf0) {
		reg_write(HOST_PARAM_2, 1);
		return;
	}
	reg_write(0x93, what);
	switch (what) {
	case 0x01: case 0x02: case 0x03: case 0x04:
		play_flags[1] = (play_flags[1] & 0x0f) | (n << 4);
		if (!(cfg & 0x04))
			stream[0].hold = n;
		break;
	case 0x05: case 0x06: case 0x07: case 0x08:
		aux_path_flags[1] = (aux_path_flags[1] & 0x0f) | (n << 4);
		break;
	case 0x11: case 0x12: case 0x13: case 0x14:
		play_flags[1] = (play_flags[1] & 0x0f) | (n << 4);
		stream[0].hold = n;
		break;
	case 0x80:
		play_flags[0] &= 0xf3;
		break;
	case 0x81:
		play_flags[0] = (play_flags[0] & 0xf3) | 0x04;
		break;
	case 0x83:
		play_flags[0] |= 0x0c;
		break;
	case 0x84:
		rec_flags[0] &= 0xf3;
		break;
	case 0x85:
		rec_flags[0] = (rec_flags[0] & 0xf3) | 0x04;
		break;
	case 0x87:
		rec_flags[0] |= 0x0c;
		break;
	case 0x88:
		aux_path_flags[0] &= 0xf3;
		cfg &= 0xf7;
		break;
	case 0x89:
		aux_path_flags[0] = (aux_path_flags[0] & 0xf3) | 0x04;
		if (((play_flags[0] >> 2) & 3) == 1 || ((rec_flags[0] >> 2) & 3) == 1)
			cfg |= 0x08;
		break;
	case 0x8b:
		aux_path_flags[0] |= 0x0c;
		break;
	}
}

static void answer_ram(uint16_t addr)
{
	uint8_t i;

	for (i = 0; i < 4; i++)
		reg_write(ANSWER(i), *(uint8_t __xdata *)(addr + i));
}

void mailbox_service(void)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t command, p1, p2;
	uint16_t v, att;

	reg_write(REG_BANK, BANK_MAIN);
	command = reg_read(HOST_COMMAND);
	REG[HEARTBEAT]++;
	/* Nothing to do: return with bank 0 selected, as the original does. */
	if (command == 0 || command == 0xee || (command & 0x01))
		return;
	reg_write(HOST_COMMAND, command | 0x01);
	p1 = reg_read(HOST_PARAM_1);
	p2 = reg_read(HOST_PARAM_2);

	switch (command) {
	case 0x2c: /* write p2 to register p1 of the test device and of the DAC */
		i2c_write_buf(TEST_DEVICE, p1, 1, &p2);
		dac_write(p1, p2);
		break;
	case 0x2e:
		i2c_read_buf(TEST_DEVICE, p1, 1, &p2);
		reg_write(ANSWER(0), p2);
		break;
	case 0x30: /* send a HID report */
		ATOMIC(aux_flags |= 0x02);
		hid_report = p1;
		reg_write(HOST_PARAM_2, 0);
		break;
	case 0x32:
		power_mode(p1);
		break;
	case 0x34: /* the original returns scratch memory here; zero in practice */
		if (p1 == 0) {
			reg_write(0x93, 0);
			reg_write(0x94, 0);
		} else if (p1 == 1) {
			reg_write(0x97, 0);
		}
		break;
	case 0x38:
		override(p1);
		reg_write(HOST_PARAM_1, 0);
		reg_write(HOST_PARAM_2, 0);
		break;
	case 0x50:
		reg_write(ANSWER(0), hid_state);
		reg_write(ANSWER(1), power_state);
		break;
	case 0x58:
		reg_write(ANSWER(0), stream[0].state);
		reg_write(ANSWER(1), spare_27e3);
		reg_write(ANSWER(2), stream[1].state);
		break;
	case 0x5a:
		reg_write(ANSWER(0), cfg & 0x03);
		reg_write(ANSWER(1), play_alt);
		reg_write(ANSWER(2), spare_27da);
		reg_write(ANSWER(3), rec_alt);
		break;
	case 0x90:
		power_state = reg_read(HOST_PARAM_1);
		break;
	case 0x91:
		reg_write(ANSWER(0), power_state);
		break;
	case 0xa0: /* a volume control's raw value and what the DAC would get */
		v = 0;
		if (p1 == 1)
			v = *(uint8_t __xdata *)(0x21c4 + 16 * p2) | ((uint16_t) * (uint8_t __xdata *)(0x21c5 + 16 * p2) << 8);
#if ROBUST
		att = (v == 0x8000) ? 0xffff : (v < 0x8000) ? 0 : (uint16_t)((0x10000UL - v) << 1);
#else
		att = (v <= 0x8000) ? 0 : (uint16_t)((0x10000UL - v) << 1);
#endif
		reg_write(ANSWER(0), (uint8_t)(v >> 8));
		reg_write(ANSWER(1), (uint8_t)v);
		reg_write(ANSWER(2), (uint8_t)(att >> 8));
		reg_write(ANSWER(3), (uint8_t)att);
		break;
	case 0xd0: /* four bytes of RAM at p1:p2 */
		answer_ram(((uint16_t)reg_read(HOST_PARAM_1) << 8) | reg_read(HOST_PARAM_2));
		break;
	case 0xd2: /* the same, address in 4-byte units */
		answer_ram((((uint16_t)reg_read(HOST_PARAM_1) << 8) | reg_read(HOST_PARAM_2)) << 2);
		break;
	case 0xd8:
#if ROBUST
		loop_mask_age = 0;
#endif
		loop_mask = reg_read(HOST_PARAM_1);
		break;
	case 0xd9:
		reg_write(ANSWER(0), loop_mask);
		break;
	case 0xda: /* echo test */
		reg_write(ANSWER(0), ~p1);
		reg_write(ANSWER(1), ~p2);
		reg_write(HOST_PARAM_1, p1 + 1);
		reg_write(HOST_PARAM_2, p2 + 1);
		break;
	}
	REG[REG_BANK] = bank;
}
