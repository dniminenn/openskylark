/* The chip raises INT0 for bus events and for control requests it has
 * already answered. The handler only records what happened. */
#include "ct7601.h"
#include "events.h"

/* bank 0 */
#define IRQ_STATUS_0 0x10
#define IRQ_STATUS_1 0x11
#define IRQ_STATUS_2 0x12
#define IRQ_LAST     0xaa /* debug: last status and a count, readable by the host */
#define IRQ_COUNT    0xab

#define S0_REQUEST   0x02 /* class request stored in bank 6 */
#define S0_SET_ALT   0x04
#define S0_SUSPEND   0x08
#define S0_WAKE      0x10
#define S0_BIT5      0x20
#define S0_RESET     0x40
#define S0_AUX       0x80
#define S1_AUX       0x01
#define S1_RESUME    0x20

/* bank 6: the SETUP packet of the request the chip just handled */
#define REQ_TYPE       0xf0
#define REQ_CODE       0xf1
#define REQ_VALUE_LOW  0xf2 /* channel, or alternate setting */
#define REQ_VALUE_HIGH 0xf3 /* control selector */
#define REQ_INDEX_LOW  0xf4 /* interface or endpoint */
#define REQ_INDEX_HIGH 0xf5 /* entity */

#define PIN_BUS_ACTIVE 0x04

__xdata volatile uint8_t events_a, events_b;
__xdata volatile uint8_t irq_seen;
__xdata volatile uint8_t pending;
__xdata volatile uint8_t power_events;
__xdata volatile uint8_t aux_flags;
__xdata volatile uint8_t play_alt;
__xdata volatile uint8_t request_entity;
#if ROBUST
__xdata volatile uint8_t alt_pending;
__xdata volatile uint8_t alt_seq;
__xdata volatile uint8_t play_closed; /* the host's last word on the playback interface was setting 0 */
__xdata volatile uint8_t records_lost;
#endif

/* gpio_set() is not reentrant under SDCC, and the main loop uses it. */
static void bus_active_pin(uint8_t on)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t out;

	REG[REG_BANK] = BANK_GPIO;
	out = REG[GPIO_OUT] & ~PIN_BUS_ACTIVE;
	if (on)
		out |= PIN_BUS_ACTIVE;
	REG[GPIO_OUT] = out;
	REG[REG_BANK] = bank;
}

static void class_request(uint8_t channel, uint8_t control, uint8_t index_low)
{
	request_entity = REG[REQ_INDEX_HIGH];
	switch (request_entity) {
	case 0x03:
		if (control == 1)
			events_b |= EV_PLAY_MUTE;
		else if (control == 2) {
			if (channel == 0)
				events_b |= EV_PLAY_VOL_ALL;
			else if (channel <= 2)
				events_b |= EV_PLAY_VOL_LR;
			else
				events_b |= EV_PLAY_VOL_REST;
		}
		break;
	case 0x05:
		events_a |= EV_PLAY_CLOCK;
		break;
	case 0x0b:
		if (control == 1)
			events_b |= EV_UNIT_0B_MUTE;
		else if (control == 2)
			events_b |= EV_UNIT_0B_VOLUME;
		break;
	case 0x0c:
		events_a |= EV_ENTITY_0C;
		break;
	case 0x0f:
		events_a |= EV_REC_CLOCK;
		break;
	case 0x13:
		if (control == 1)
			events_b |= EV_UNIT_13_MUTE;
		else if (control == 2)
			events_b |= EV_UNIT_13_VOLUME;
		break;
	case 0x15:
		events_a |= EV_ENTITY_15;
		break;
	}
	/* UAC1 sets the sampling frequency on an endpoint instead. */
	if (control == 1) {
		if (index_low == 0x01) {
			request_entity = 0x05;
			events_a |= EV_PLAY_CLOCK;
		} else if (index_low == 0x82) {
			request_entity = 0x0f;
			events_a |= EV_REC_CLOCK;
		} else if (index_low == 0x04) {
			request_entity = 0x15;
			events_a |= EV_ENTITY_15;
		}
	}
}

/* Handles one snapshot of the interrupt causes and acknowledges them. */
static void causes(uint8_t s0, uint8_t s1)
{
	uint8_t value_low, value_high, index_low, n;
#if ROBUST
	uint8_t type, code;

	/* There is one request record, and the chip overwrites it with each
	 * request. Acknowledge before reading it: a request that lands in
	 * between then raises its cause again and is read on the next pass,
	 * where the vendor order acknowledged it away unread. */
	REG[REG_BANK] = BANK_MAIN;
	REG[IRQ_STATUS_0] = s0;
	REG[IRQ_STATUS_1] = s1;
#endif

	if (s0 & S0_RESET) {
		irq_seen |= 0x01;
		events_a |= EV_BUS_RESET;
		bus_active_pin(1);
	}
	if (s0 & S0_SUSPEND) {
		irq_seen |= 0x02;
		power_events |= 0x01;
	}
	if (s0 & S0_WAKE) {
		irq_seen |= 0x04;
		pending |= 0x80;
	}
	if (s0 & S0_BIT5)
		irq_seen |= 0x08;
	if ((s1 & S1_AUX) || (s0 & S0_AUX)) {
		irq_seen |= 0x40;
		aux_flags |= 0x80;
	}
	if (s1 & S1_RESUME) {
		irq_seen |= 0x20;
		power_events |= 0x02;
		bus_active_pin(0);
	}

	REG[REG_BANK] = 0x06;
	value_low = REG[REQ_VALUE_LOW];
	index_low = REG[REQ_INDEX_LOW];
	value_high = REG[REQ_VALUE_HIGH];
	if (pending & 0x80) {
		n = REG[REQ_INDEX_HIGH];
		if (n > 0 && n < 5)
			pending |= 1 << (n - 1);
	}
#if ROBUST
	/* The record may by now belong to a later request than the cause
	 * being handled. Act on it only if it is the kind of request the
	 * cause says, and count the times it was not; settings_watch() makes
	 * good a class request missed this way. */
	type = REG[REQ_TYPE];
	code = REG[REQ_CODE];
	if (s0 & S0_SET_ALT) {
		if (type != 0x01 || code != 0x0b) {
			if (records_lost != 0xff)
				records_lost++;
		} else if (value_low > 0 && value_low <= ALT_MAX && index_low == 1) {
			/* The vendor code wrote the alternate setting into a
			 * byte the main loop also rewrites, so a change arriving
			 * while the previous one was being applied was lost.
			 * Keep it in a byte only this handler writes, and count
			 * changes so the main loop can tell. */
			alt_pending = value_low;
			alt_seq++;
			play_closed = 0;
			events_a |= EV_PLAY_ALT;
		} else if (value_low == 0 && index_low == 1) {
			/* The host is done with the stream; see stream.c. */
			play_closed = 1;
		}
	}
	if (s0 & S0_REQUEST) {
		if ((type & 0x60) == 0x20 && code == 0x01)
			class_request(value_low, value_high, index_low);
		else if (records_lost != 0xff)
			records_lost++;
	}
	REG[REG_BANK] = BANK_MAIN;
#else
	if ((s0 & S0_SET_ALT) && value_low > 0 && index_low == 1) {
		play_alt = (play_alt & 0xf0) | value_low;
		events_a |= EV_PLAY_ALT;
	}
	if (s0 & S0_REQUEST)
		class_request(value_low, value_high, index_low);

	REG[REG_BANK] = BANK_MAIN;
	REG[IRQ_STATUS_0] = s0;
	REG[IRQ_STATUS_1] = s1;
#endif
	REG[IRQ_COUNT]++;
	REG[IRQ_LAST] = s0;
}

#if ROBUST
/* INT0 is edge-triggered. The vendor handler takes one snapshot, acknowledges
 * it and then discards any edge latched meanwhile, so a cause arriving while
 * it runs can go unserviced, for good if the chip holds the line until every
 * cause is acknowledged. Go round while any cause is left. The latch is
 * cleared before each look, so a cause arriving after the last one still
 * brings the handler back, and cleared again on giving up, so a cause that
 * will not clear cannot keep the processor in here. */
static void service_causes(void)
{
	uint8_t pass, s0, s1;

	for (pass = 0; pass < 4; pass++) {
		IE0 = 0;
		REG[REG_BANK] = BANK_MAIN;
		s0 = REG[IRQ_STATUS_0];
		s1 = REG[IRQ_STATUS_1];
		(void)REG[IRQ_STATUS_2];
		if (pass && !(s0 | s1))
			return;
		causes(s0, s1);
	}
	IE0 = 0;
}

void int0_isr(void) __interrupt(0)
{
	uint8_t bank = REG[REG_BANK];

	service_causes();
	REG[REG_BANK] = bank;
}

/* From the main loop: picks up a cause the handler was not called for. */
void irq_poll(void)
{
	uint8_t bank, s0, s1;

	EA = 0;
	bank = REG[REG_BANK];
	REG[REG_BANK] = BANK_MAIN;
	s0 = REG[IRQ_STATUS_0];
	s1 = REG[IRQ_STATUS_1];
	if (s0 | s1)
		service_causes();
	REG[REG_BANK] = bank;
	EA = 1;
}
#else
void int0_isr(void) __interrupt(0)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t s0, s1;

	REG[REG_BANK] = BANK_MAIN;
	s0 = REG[IRQ_STATUS_0];
	s1 = REG[IRQ_STATUS_1];
	(void)REG[IRQ_STATUS_2];
	causes(s0, s1);
	REG[REG_BANK] = bank;
	IE0 = 0;
}
#endif
