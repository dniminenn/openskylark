/* Tables in bank 6 that let the chip answer audio-class control requests and
 * run its endpoints without the firmware: each entry points at a buffer in
 * RAM (address in 4-byte units). Field meanings are inferred. */
#include "ct7601.h"

#define RAM_END 0x3000

static void store(uint16_t addr, uint8_t value)
{
	if (addr < RAM_END)
		*(uint8_t __xdata *)addr = value;
}

/* One class request the chip may answer from RAM. */
struct request {
	uint16_t slot;   /* response buffer */
	uint8_t flags;   /* high nibble of the pointer's top byte */
	uint8_t entity;  /* clock source 5 (playback), 15 (capture), ... */
	uint8_t control; /* 1 = sampling frequency, 2 = clock validity / alt setting */
	uint8_t arg, kind;
};

#define ALT_SETTINGS_VALID 0x001f /* alternate settings 0-4 */

static __code const struct request requests_uac2[] = {
	{0x0800, 0x70, 0x05, 0x01, 0x00, 0x10},
	{0x0801, 0xb0, 0x05, 0x01, 0x00, 0x10},
	{0x0820, 0x70, 0x0f, 0x01, 0x00, 0x10},
	{0x0821, 0xb0, 0x0f, 0x01, 0x00, 0x10},
	{0x0860, 0x50, 0x05, 0x02, 0x00, 0x10},
	{0x0864, 0x50, 0x0f, 0x02, 0x00, 0x10},
	{0x0861, 0x50, 0x00, 0x02, 0x00, 0x11},
	{0x0865, 0x50, 0x00, 0x02, 0x00, 0x12},
	{0x0867, 0x70, 0x0c, 0x01, 0x00, 0x10},
};

static __code const struct request requests_uac1[] = {
	{0x0800, 0x70, 0x00, 0x01, 0x00, 0x21},
	{0x0820, 0x70, 0x00, 0x01, 0x00, 0x22},
};

#define REQUEST_BASE 0x3c

static void request_entry(uint8_t reg, const struct request __code *r)
{
	reg_write(reg, (uint8_t)r->slot);
	reg_write(reg + 1, (uint8_t)(r->slot >> 8));
	reg_update(reg + 1, r->flags, 0xf0);
	reg_write(reg + 2, r->entity);
	reg_write(reg + 3, r->control);
	reg_write(reg + 4, r->arg);
	reg_write(reg + 5, r->kind);
}

/* Leaves bank 6 selected, as the original does. */
void control_table_init(void)
{
	uint8_t i, reg = REQUEST_BASE;
	uint16_t at;

	reg_write(REG_BANK, 0x06);
	if (cfg & CFG_UAC1) {
		for (i = 0; i < 2; i++, reg += 6)
			request_entry(reg, &requests_uac1[i]);
		return;
	}
	for (i = 0; i < 9; i++, reg += 6) {
		request_entry(reg, &requests_uac2[i]);
		at = requests_uac2[i].slot << 2;
		switch (i) {
		case 4:
		case 5:
			store(at, 1); /* clock valid */
			break;
		case 6:
		case 7:
			store(at, 2);
			store(at + 1, (uint8_t)ALT_SETTINGS_VALID);
			store(at + 2, (uint8_t)(ALT_SETTINGS_VALID >> 8));
			store(at + 3, 0);
			store(at + 4, 0);
			break;
		case 8:
			store(at, 0);
			break;
		}
	}
}

/* Endpoint blocks: buffer slot, then five configuration bytes. */
struct endpoint {
	uint8_t reg;
	uint16_t slot;
	uint8_t config[5];
};

static __code const struct endpoint endpoints_a[] = {
	{0xa8, 0x0870, {0x03, 0x01, 0x00, 0x01, 0x00}},
	{0xaf, 0x0898, {0x0b, 0x01, 0x00, 0x01, 0x00}},
};
static __code const struct endpoint endpoints_b[] = {
	{0xc4, 0x0871, {0x03, 0xff, 0x01, 0xff, 0x01}},
	{0xcb, 0x0899, {0x0b, 0xff, 0x01, 0xff, 0x01}},
};

static void endpoint_entry(const struct endpoint __code *e)
{
	uint8_t i;

	reg_write(e->reg, (uint8_t)e->slot);
	reg_write(e->reg + 1, (uint8_t)(e->slot >> 8));
	for (i = 0; i < 5; i++)
		reg_write(e->reg + 2 + i, e->config[i]);
}

/* Both leave bank 6 selected. */
void endpoint_table_a_init(void)
{
	uint8_t i;

	for (i = 0; i < 2; i++) {
		reg_write(REG_BANK, 0x06);
		endpoint_entry(&endpoints_a[i]);
		store(endpoints_a[i].slot << 2, 0);
	}
}

void endpoint_table_b_init(void)
{
	reg_write(REG_BANK, 0x06);
	endpoint_entry(&endpoints_b[0]);
	endpoint_entry(&endpoints_b[1]);
}

/* Sample rates are numbered 0-14: {32000, 44100, 48000} doubled index/3
 * times. rate_mask has one bit per number. */
static uint32_t rate_hz(uint8_t index)
{
	static __code const uint32_t base[3] = {32000, 44100, 48000};

	return base[index % 3] << (index / 3);
}

#define RATE_TABLE 0x2000

static uint16_t store32(uint16_t at, uint32_t v)
{
	store(at++, (uint8_t)v);
	store(at++, (uint8_t)(v >> 8));
	store(at++, (uint8_t)(v >> 16));
	store(at++, (uint8_t)(v >> 24));
	return at;
}

/* The clock source's answers: the current frequency, then the list of
 * supported ones as min/max/step triples. */
void rate_table_init(void)
{
	uint16_t mask = ((uint16_t)rate_mask[0] << 8) | rate_mask[1];
	uint16_t at = RATE_TABLE;
	uint8_t i, count = 0, first = 1;

	for (i = 0; i < 15; i++)
		if (mask & (1u << i))
			count++;
	for (i = 0; i < 15; i++) {
		uint32_t hz;

		if (!(mask & (1u << i)))
			continue;
		hz = rate_hz(i);
		if (first) {
			at = store32(at, hz);
			store(at++, count);
			store(at++, 0);
			first = 0;
		}
		at = store32(at, hz);
		at = store32(at, hz);
		at = store32(at, 0);
	}
}

#define VOLUME_RANGE 0x21c4
#define VOLUME_MIN   0xc080 /* -63.5 dB */
#define VOLUME_MAX   0x0000
#define VOLUME_STEP  0x0080 /* 0.5 dB */

static uint16_t store16(uint16_t at, uint16_t v)
{
	store(at++, (uint8_t)v);
	store(at++, (uint8_t)(v >> 8));
	return at;
}

/* The feature unit's volume range, once per control (master, left, right). */
void volume_range_init(void)
{
	uint16_t at = VOLUME_RANGE;
	uint8_t i;

	for (i = 0; i < 3; i++) {
		at = store16(at, 0);
		at = store16(at, 0);
		if (cfg & CFG_UAC1) {
			at = store16(at, VOLUME_MIN);
			at = store16(at, 0);
			at = store16(at, VOLUME_MAX);
			at = store16(at, 0);
			at = store16(at, VOLUME_STEP);
			at = store16(at, 0);
		} else {
			at = store16(at, 1);
			at = store16(at, VOLUME_MIN);
			at = store16(at, VOLUME_MAX);
			at = store16(at, VOLUME_STEP);
			at = store16(at, 0);
			at = store16(at, 0);
		}
	}
}
