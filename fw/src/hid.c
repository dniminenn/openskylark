/* The HID channel: bank 0x0b moves 8-byte frames through a buffer in RAM.
 * It carries button reports to the host and command frames from it. */
#include "ct7601.h"
#include "events.h"

#define FRAME ((volatile uint8_t __xdata *)0x2680)

#define HID_BANK   0x0b
#define HID_SEND   0x07 /* bit 0 starts a report */
#define HID_STATUS 0x08 /* bit 0 set when it has gone */
#define REPORT_ECHO 0x92 /* bank 0: the report a host command asked for */

#define BUTTON_PINS 0x27 /* bank 2 register 0x0c, active low */

__xdata uint8_t hid_state;   /* 1 idle, 2-3 sending the report, 4-5 sending the release; 0 off */
__xdata uint8_t hid_report;
__xdata uint8_t buttons[2];
__xdata uint8_t hold_count;
__xdata uint8_t long_press;

void update_mode_enter(void);

/* Called every 21 slow ticks. A press of 6 to 9 polls counts on release; a
 * longer one counts every 10 polls while held. */
void buttons_poll(void)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t pressed;

	REG[REG_BANK] = BANK_GPIO;
	pressed = ~REG[0x0c] & BUTTON_PINS;
	if (pressed) {
		if (hold_count != 0xff) {
			buttons[0] = 0;
			buttons[1] = pressed;
			if (++hold_count >= 10) {
				long_press = 1;
				hold_count = 0;
				usb_flags |= 0x01;
			}
		}
	} else if (long_press) {
		long_press = 0;
		hold_count = 0;
	} else {
		if (hold_count > 5)
			usb_flags |= 0x01;
		hold_count = 0;
	}
	REG[REG_BANK] = bank;
}

static void report_send(uint8_t report)
{
	uint8_t bank = REG[REG_BANK];

	FRAME[0] = report;
	reg_write(REG_BANK, HID_BANK);
	reg_update(HID_SEND, 0x01, 0x01);
	reg_write(REG_BANK, bank);
}

static uint8_t report_sent(void)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t done = 0;

	reg_write(REG_BANK, HID_BANK);
	if (reg_read(HID_STATUS) & 0x01) {
		reg_update(HID_STATUS, 0x00, 0x01);
		done = 1;
	}
	reg_write(REG_BANK, bank);
	return done;
}

void aux_service(void)
{
	uint8_t bank = REG[REG_BANK];

	REG[REG_BANK] = BANK_MAIN;
	if (aux_flags & 0x80) {
		if (FRAME[0] == 0x5a && FRAME[1] == 0xb0 && FRAME[2] == 0xca && FRAME[3] == 0xfe) {
			FRAME[3] = 0;
			FRAME[2] = 0;
			FRAME[1] = 0;
			FRAME[0] = 0;
			update_mode_enter();
		}
		/* Frames starting c7 a5 are checked by the original and then
		 * ignored: the hook they would reach is empty in this build. */
		ATOMIC(aux_flags &= 0x7f);
	}
	switch (hid_state) {
	case 1:
		if (!(aux_flags & 0x03))
			break;
		if (aux_flags & 0x01)
			hid_report = 0; /* the original maps no buttons to report bits */
		else
			reg_write(REPORT_ECHO, hid_report);
		ATOMIC(aux_flags &= 0xfc);
		if (hid_report & 0x77)
			hid_state = 2;
		break;
	case 2:
		report_send(hid_report);
		hid_state = 3;
		break;
	case 3:
		if (report_sent())
			hid_state = 4;
		break;
	case 4:
		report_send(0);
		hid_state = 5;
		break;
	case 5:
		if (report_sent())
			hid_state = 1;
		break;
	}
	REG[REG_BANK] = bank;
}
