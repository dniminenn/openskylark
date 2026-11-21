/* What main() does on every pass. */
#include "ct7601.h"
#include "events.h"

/* bank 0, visible to the host */
#define STATUS_RATE    0xa4
#define STATUS_FLAGS_B 0xa5
#define STATUS_PATH    0xa6
#define STATUS_BYTES   0xa7

__xdata uint8_t loop_mask;    /* set bits switch parts of the main loop off */
#if ROBUST
__xdata uint8_t loop_mask_age; /* slow ticks since the host last set loop_mask */
#endif
__xdata uint8_t rec_alt;      /* like play_alt, for the record path */
__xdata uint8_t misc_flags;   /* low nibble to STATUS_FLAGS_B, top two bits to STATUS_PATH */

static uint8_t usb_speed_now(void)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t v;

	reg_write(REG_BANK, BANK_MAIN);
	v = reg_read(0x32);
	REG[REG_BANK] = bank;
	return (v & 0x10) ? 1 : 0;
}

static void speed_update(void)
{
	cfg = (cfg & ~CFG_HIGH_SPEED) | (usb_speed_now() << 1);
	pll_setup((cfg >> 1) & 1);
}

/* Entity 0x0c holds one byte; bank 5 register 0x0f bit 3 follows it. */
static void entity0c_event(void)
{
	uint8_t v, bank;

	if (request_entity != 0x0c)
		return;
	v = *(volatile uint8_t __xdata *)0x219c;
	bank = reg_read(REG_BANK);
	reg_write(REG_BANK, 0x05);
	reg_update(0x0f, (v == 1) ? 0x08 : 0x00, 0x08);
	reg_write(REG_BANK, bank);
}

/* Takes each event the interrupt handler recorded, one bit at a time and in
 * the original's order, then runs the playback state machine. */
static void event_service(void)
{
	static __code const uint8_t order_a[] = {EV_PLAY_CLOCK, EV_REC_CLOCK, EV_ENTITY_15, EV_PLAY_ALT, 0x04, 0x08};
	static __code const uint8_t which_a[] = {1, 2, 2, 1, 2, 2};
	uint8_t i, bit;
#if ROBUST
	uint8_t a, b;

	/* Take the recorded events with interrupts off, so one arriving while
	 * they are being cleared is not lost, then work from the copy. */
	EA = 0;
	a = events_a;
	events_a = 0;
	b = events_b;
	events_b = 0;
	EA = 1;
	if (a & EV_BUS_RESET) {
		path_state |= 0x01;
		speed_update();
		/* Whatever was playing is over; the vendor code waits for the
		 * data to stop before it mutes. */
		stream_reset(0);
	}
	for (i = 0; i < sizeof order_a; i++)
		if (a & order_a[i])
			interface_event(which_a[i]);
	if (a & EV_ENTITY_0C)
		entity0c_event();
	for (bit = 1; bit; bit <<= 1)
		if (b & bit)
			request_event(bit);
	stream_service(0);
	return;
#else
	if (events_a) {
		if (events_a & EV_BUS_RESET) {
			events_a &= ~EV_BUS_RESET;
			path_state |= 0x01;
			speed_update();
		}
		for (i = 0; i < sizeof order_a; i++) {
			if (events_a & order_a[i]) {
				events_a &= ~order_a[i];
				interface_event(which_a[i]);
			}
		}
		if (events_a & EV_ENTITY_0C) {
			events_a &= ~EV_ENTITY_0C;
			entity0c_event();
		}
	}
	if (events_b) {
		for (bit = 1; bit; bit <<= 1) {
			if (events_b & bit) {
				events_b &= ~bit;
				request_event(bit);
			}
		}
	}
	stream_service(0);
#endif
}

/* Rate index as the host tools expect it: PCM 0-9, DoP 0-3, DSD 8-11. */
static uint8_t status_rate(uint8_t rate, uint8_t status)
{
	if (status & 0x02)
		return (uint8_t)(((uint16_t)rate - 4) >> 1);
	if (status & 0x04)
		return (uint8_t)(((uint16_t)rate - 2) >> 1) | 0x08;
	return rate;
}

static void status_publish(void)
{
	uint8_t bank = REG[REG_BANK];

	reg_write(REG_BANK, BANK_MAIN);
	(void)status_rate(rec_alt >> 4, stream[1].status); /* computed and dropped, as in the original */
	reg_write(STATUS_RATE, status_rate(play_alt >> 4, stream[0].status) & 0x0f);
	reg_write(STATUS_FLAGS_B, misc_flags & 0x0f);
	if (!(stream[0].status & 0x01))
		path_state |= PATH_PLAY_MUTED;
	if (!(stream[1].status & 0x01))
		path_state |= PATH_REC_MUTED;
	reg_write(STATUS_PATH, (path_state & 0x1f) | (misc_flags & 0xc0));
	reg_write(STATUS_BYTES, path_bits & 0x1f);
	REG[REG_BANK] = bank;
}

void main_loop(void)
{
	for (;;) {
		do {
			mailbox_service();
			if (!(loop_mask & 0x01))
				event_service();
			if (!(loop_mask & 0x02)) {
				aux_service();
				usb_flags &= ~0x01;
				status_publish();
			}
			if (!(loop_mask & 0x04))
				power_service();
			if (!(loop_mask & 0x80))
				flags_service();
			if (irq_seen)
				irq_seen = 0;
		} while (!slow_flag);
		buttons_poll();
#if ROBUST
		/* A host tool masks parts of the loop to keep the firmware off
		 * the I2C bus while it uses it. The vendor code stays masked
		 * until told otherwise, so a tool that dies leaves streams
		 * unhandled until the next plug-in. Give up after 2 s. */
		if (!loop_mask)
			loop_mask_age = 0;
		else if (++loop_mask_age > 95)
			loop_mask = 0;
		irq_poll();
		/* The vendor code reads the negotiated speed once, when the
		 * bus reset is handled, and stays on the full-speed clock path
		 * if the host had not finished negotiating by then. */
		if (!(cfg & CFG_HIGH_SPEED) && usb_speed_now())
			speed_update();
		settings_watch();
		playback_level_watch();
#endif
		slow_flag = 0;
	}
}
