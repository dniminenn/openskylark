/* Suspend and resume, and the wake-source flags. */
#include "ct7601.h"
#include "events.h"

/* bank 0 */
#define BUS_LINE   0x32 /* low nibble: line state */
#define BUS_STATE  0x33
#define USB_CTL    0x30
#define WAKE_CTL   0x31
#define LAST_LINE  0xa8 /* debug: what ended the last suspend; also the last wake flags */
#define LAST_STATE 0xa9

#define PIN_BUS_ACTIVE 0x04

__xdata uint8_t power_state; /* main() sets it to 1 */
__xdata uint8_t resume_count;
#if ROBUST
static __xdata uint8_t resumed; /* the DAC and the path are still to be put back */
#endif

static uint8_t bus_read(uint8_t reg)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t v;

	reg_write(REG_BANK, BANK_MAIN);
	v = reg_read(reg);
	REG[REG_BANK] = bank;
	return v;
}

static void power_down(void)
{
	uint8_t bank = REG[REG_BANK];

	reg_write(REG_BANK, BANK_MAIN);
	reg_write(0x01, 0xa4);
	reg_write(REG_BANK, 0x01);
	reg_update(0x01, 0x00, 0x07);
	reg_write(REG_BANK, 0x03);
	reg_update(0x02, 0xe0, 0xe0);
	reg_update(0x1e, 0x08, 0x08);
	reg_write(REG_BANK, 0x04);
	reg_update(0x02, 0xe0, 0xe0);
	reg_update(0x1e, 0x08, 0x08);
	reg_write(REG_BANK, 0x02);
	reg_update(0x18, 0x00, 0x40);
	reg_update(0x18, 0x80, 0x80);
	REG[REG_BANK] = bank;
}

static void power_up(void)
{
	uint8_t bank = REG[REG_BANK];

	reg_write(REG_BANK, 0x01);
	reg_update(0x01, 0x02, 0x07);
	reg_write(REG_BANK, 0x03);
	reg_update(0x02, 0x00, 0xe0);
	reg_update(0x1e, 0x00, 0x08);
	reg_write(REG_BANK, 0x04);
	reg_update(0x02, 0x00, 0xe0);
	reg_update(0x1e, 0x00, 0x08);
	reg_write(REG_BANK, BANK_MAIN);
	reg_write(0x01, 0x20);
	REG[REG_BANK] = bank;
}

/* Powers the audio blocks down and sits with interrupts off until the bus
 * leaves the suspended line state. */
static void suspend_wait(void)
{
	uint8_t bank, line, state;

	EA = 0;
	power_down();
	bank = REG[REG_BANK];
	do {
		REG[REG_BANK] = BANK_MAIN;
		state = REG[BUS_STATE];
		line = REG[BUS_LINE] & 0x0f;
	} while (!(state & 0x01) || line == 3);
	REG[LAST_LINE] = line;
	REG[LAST_STATE] = state;
	REG[REG_BANK] = bank;
	power_up();
	EA = 1;
}

/* True if the event is waiting, in which case every waiting event is
 * discarded, as the vendor code does. The vendor code tests and clears in
 * two steps and can discard one that arrives in between. */
static uint8_t power_event_take(uint8_t bit)
{
	uint8_t had;

#if ROBUST
	EA = 0;
#endif
	had = power_events & bit;
	if (had)
		power_events = 0;
#if ROBUST
	EA = 1;
#endif
	return had;
}

void power_service(void)
{
	uint8_t line = bus_read(BUS_LINE) & 0x0f;

	(void)bus_read(BUS_STATE);
	switch (power_state) {
	case 1:
		if (power_event_take(0x01)) {
			power_state = 2;
		} else if (line == 2) {
			ATOMIC(power_events = 0);
			power_state = 2;
		}
		break;
	case 2: {
		uint8_t bank = REG[REG_BANK];

		reg_write(REG_BANK, BANK_MAIN);
		reg_update(USB_CTL, 0x04, 0x04);
		REG[REG_BANK] = bank;
		power_state = 3;
		break;
	}
	case 3:
		if (power_event_take(0x02))
			power_state = 8;
		break;
	case 8: {
		uint8_t bank = REG[REG_BANK];

		reg_write(REG_BANK, BANK_MAIN);
		REG[REG_BANK] = bank;
		path_state &= ~0x01;
		power_state = 9;
		break;
	}
	case 9:
#if ROBUST
		/* The vendor code stops the DAC's clocks with the output as it
		 * was and brings them back without checking anything. Mute
		 * first. Putting things back waits until the bus has been
		 * re-entered (state 11): the line states that follow are only
		 * there for a moment, and the DAC's clock may not be yet. */
		telemetry.suspends++;
		path_mute(0, 1);
		suspend_wait();
		resumed = 1;
#else
		suspend_wait();
#endif
		(void)bus_read(BUS_LINE);
		(void)bus_read(BUS_STATE);
		resume_count = 0;
		power_state = 10;
		break;
	case 10:
		if (line == 2 || line == 1)
			resume_count++;
		else if (resume_count)
			resume_count = 0;
		if (resume_count > 20)
			power_state = 11;
		break;
	case 11:
#if ROBUST
		/* cfg holds the speed last negotiated, not what the device can
		 * do; choosing by it turns one full-speed session into
		 * full speed until power is removed. */
		usb_mode(usb_high_speed_capable ? 3 : 2);
		/* The vendor code marks the bus active again only at a bus
		 * reset, so after a plain resume the pin and the published
		 * state say "disconnected" for the rest of the session. */
		path_state |= 0x01;
		gpio_set(PIN_BUS_ACTIVE, 1);
		if (resumed) {
			/* Assume the DAC may have lost its settings and the path
			 * its clock setup: write both again and start over. */
			resumed = 0;
			dac_restore();
			stream_reset(0);
			ATOMIC(events_a |= EV_PLAY_ALT);
		}
#else
		usb_mode((cfg & CFG_HIGH_SPEED) ? 3 : 2);
#endif
		power_state = 1;
		break;
	}
}

/* Applies the wake flags the interrupt handler collected. */
void flags_service(void)
{
	uint8_t bank;
#if ROBUST
	uint8_t pending_now;

	/* Work from a copy taken with interrupts off, so a flag the handler
	 * adds while these are being applied is kept for the next pass. */
	EA = 0;
	pending_now = pending;
	pending = 0;
	EA = 1;
#define pending pending_now
#endif

	if (!pending)
		return;
	bank = REG[REG_BANK];
	if (pending & 0x08) {
		reg_write(REG_BANK, 0x0f);
		reg_update(0x0b, 0x08, 0x08);
	}
	reg_write(REG_BANK, BANK_MAIN);
	if (pending & 0x01)
		reg_update(WAKE_CTL, 0x02, 0x02);
	if (pending & 0x02)
		reg_update(WAKE_CTL, 0x04, 0x04);
	if (pending & 0x04)
		reg_update(WAKE_CTL, 0x01, 0x01);
	reg_write(LAST_LINE, pending);
#if ROBUST
#undef pending
#else
	pending = 0;
#endif
	REG[REG_BANK] = bank;
}
