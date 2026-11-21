/* ESS ES9018K2M behind the bridge's I2C master. */
#include "ct7601.h"
#include "events.h"

#define DAC_ADDR 0x90

#define DAC_MUTE_REG  7
#define DAC_MUTE_BITS 0x03
#define DAC_VOLUME_L  15
#define DAC_VOLUME_R  16
#define VOLUME_OFF    0xff

__xdata uint8_t volume_left, volume_right;

/* Register values written at power-up, in order. */
/* Register 10, low four bits: how many sample periods the DAC takes to lock
 * to its input, 16384 at 0 down to 1024 at 15. The bridge upsets the DAC's
 * lock whenever data resumes after a gap, and at the vendor's 0 the DAC then
 * stays silent for a third of a second at 48 kHz. */
#if ROBUST
#define DAC_LOCK_SPEED 0x0f
#else
#define DAC_LOCK_SPEED 0x00
#endif

static __code const uint8_t defaults[][2] = {
	{0, 0xf0}, {1, 0x8c}, {4, 0x0a}, {5, 0xee}, {6, 0x4a}, {7, 0x83},
	{8, 0x10}, {10, DAC_LOCK_SPEED}, {11, 0x02}, {12, 0xbe}, {13, 0x00}, {14, 0x8a},
	{15, 0xff}, {16, 0xff}, {17, 0xff}, {18, 0xff}, {19, 0xff}, {20, 0x66},
	{21, 0x00}, {22, 0x00}, {23, 0x00}, {24, 0x00}, {25, 0x00},
};

#if ROBUST
__xdata uint8_t dac_faults; /* writes that were never acknowledged, and failed verifies; stops at 255 */

static void fault(void)
{
	if (dac_faults != 0xff)
		dac_faults++;
}

/* Set by dac_read() and dac_write(). Volatile because SDCC otherwise assumes
 * a call leaves a file-scope bit alone and drops the test after it. */
static volatile __bit read_ok;        /* the last dac_read was acknowledged and finished */
static volatile __bit last_write_ok;
static volatile __bit write_timed_out;
/* What each register was last written with. */
static __xdata uint8_t shadow[26];
#endif

static uint8_t dac_read(uint8_t reg)
{
	uint8_t bank = reg_read(REG_BANK);
	uint8_t value;

	reg_write(REG_BANK, BANK_I2C);
#if ROBUST
	/* The vendor read looks at no acknowledge, so a read nobody answered
	 * returns whatever the receive register last held. */
	value = 0;
	i2c_start();
	read_ok = i2c_send(DAC_ADDR);
	if (read_ok) {
		i2c_send(reg);
		i2c_start();
		read_ok = i2c_send(DAC_ADDR | 1);
	}
	if (read_ok) {
		value = i2c_recv(1);
		if (i2c_timed_out)
			read_ok = 0;
	}
	i2c_stop();
#else
	i2c_start();
	i2c_send(DAC_ADDR);
	i2c_send(reg);
	i2c_start();
	i2c_send(DAC_ADDR | 1);
	value = i2c_recv(1);
	i2c_stop();
#endif
	reg_write(REG_BANK, bank);
	return value;
}

void dac_write(uint8_t reg, uint8_t value)
{
	uint8_t bank = reg_read(REG_BANK);
#if ROBUST
	uint8_t tries;
	__bit ok = 0, reg_acked, value_acked;

	/* The vendor code sends the three bytes and never looks at the
	 * acknowledges. Send them the same way and repeat the write if one
	 * was missed. Only an unanswered address ends an attempt early, and a
	 * bus on which a byte never finishes is not tried again. */
	reg_write(REG_BANK, BANK_I2C);
	for (tries = 0; tries < 3 && !ok; tries++) {
		i2c_start();
		if (i2c_send(DAC_ADDR)) {
			reg_acked = i2c_send(reg);
			value_acked = i2c_send(value);
			ok = reg_acked && value_acked;
		}
		i2c_stop();
		if (i2c_timed_out)
			break;
	}
	if (!ok)
		fault();
	last_write_ok = ok;
	write_timed_out = i2c_timed_out;
	if (reg < sizeof shadow)
		shadow[reg] = value;
#else
	reg_write(REG_BANK, BANK_I2C);
	i2c_start();
	i2c_send(DAC_ADDR);
	i2c_send(reg);
	i2c_send(value);
	i2c_stop();
#endif
	reg_write(REG_BANK, bank);
}

#if ROBUST
/* Register 7 holds the mute bits beside filter settings the host may have
 * changed. The vendor code reads it back once and rewrites it, so one bad
 * read sticks in the filter bits until power is removed. Read it twice and
 * believe it only if both agree and the reserved bits are right; otherwise
 * use the value last written. */
static void dac_mute_bits(uint8_t bits)
{
	uint8_t first = dac_read(DAC_MUTE_REG);
	__bit first_ok = read_ok;
	uint8_t second = dac_read(DAC_MUTE_REG);

	if (!first_ok || !read_ok || first != second || (first & 0x90) != 0x80)
		first = shadow[DAC_MUTE_REG] | 0x80;
	dac_write(DAC_MUTE_REG, (first & ~DAC_MUTE_BITS) | bits);
}

/* Writes every register in the start-up table, with the table's values or
 * (restore) with what each was last written, so that settings the host made
 * survive a resume. Then reads them all back and repeats the lot if the DAC
 * does not hold them; a matching read-back counts whatever the
 * acknowledges said. Gives up at once on a bus where nothing moves. */
static void dac_load(__bit restore)
{
	uint8_t i, tries, reg;
	__bit good;

	for (tries = 0; tries < 3; tries++) {
		for (i = 0; i < sizeof defaults / sizeof defaults[0]; i++) {
			reg = defaults[i][0];
			dac_write(reg, restore ? shadow[reg] : defaults[i][1]);
			if (write_timed_out)
				return;
		}
		good = 1;
		for (i = 0; i < sizeof defaults / sizeof defaults[0]; i++) {
			reg = defaults[i][0];
			if (dac_read(reg) != shadow[reg] || !read_ok)
				good = 0;
		}
		if (good)
			return;
		fault();
	}
}

void dac_configure(void)
{
	dac_load(0);
}

void dac_restore(void)
{
	dac_load(1);
}

/* Waits, for at most 300 ms, for the DAC to report lock on the incoming
 * clocks. */
static void dac_wait_lock(void)
{
	uint8_t n;

	for (n = 0; n < 30; n++) {
		if ((dac_read(64) & 0x01) || !read_ok)
			return;
		delay(20);
	}
}
#else
static void dac_update(uint8_t reg, uint8_t value, uint8_t mask)
{
	uint8_t old = dac_read(reg);

	dac_write(reg, (old & ~mask) | (value & mask));
}

static void dac_mute_bits(uint8_t bits)
{
	dac_update(DAC_MUTE_REG, bits, DAC_MUTE_BITS);
}

/* Writes the start-up register values and leaves the DAC muted. */
void dac_configure(void)
{
	uint8_t i;

	for (i = 0; i < sizeof defaults / sizeof defaults[0]; i++)
		dac_write(defaults[i][0], defaults[i][1]);
}
#endif

void dac_init(void)
{
	delay(200);
	delay(200);
	dac_configure();
	delay(100);
	gpio_set(PIN_DAC_UP, 1);
}

void dac_set_volume(uint8_t left, uint8_t right)
{
	dac_write(DAC_VOLUME_L, left);
	dac_write(DAC_VOLUME_R, right);
}

void dac_mute(uint8_t on)
{
	if (on) {
		gpio_set(PIN_MUTE, 1);
		dac_mute_bits(DAC_MUTE_BITS);
		dac_set_volume(VOLUME_OFF, VOLUME_OFF);
		return;
	}
	if (!stream_format)
		return;
	delay(200);
#if ROBUST
	playback_settle();
	/* A format or rate change that arrived during the wait is about to
	 * restart the stream; do not open the output on the old one. */
	if (events_a & (EV_PLAY_ALT | EV_PLAY_CLOCK))
		return;
	dac_wait_lock();
#endif
	gpio_set(PIN_MUTE, 0);
	dac_mute_bits(0);
	dac_set_volume(volume_left, volume_right);
}

/* The shipped firmware does nothing in these three: the DAC is left on
 * auto-detect (register 1 = 0x8c) to tell I2S, SPDIF and DSD apart from the
 * signal alone. The robust build tells it instead. They are called while the
 * output is muted, before the path is switched and unmuted. */
#ifndef DAC_EXPLICIT_INPUT
#define DAC_EXPLICIT_INPUT ROBUST
#endif
#define DAC_INPUT_REG 1
#define DAC_INPUT_I2S 0x80 /* 32-bit I2S, auto-detect off */
#define DAC_INPUT_DSD 0x83

void dac_format_pcm(uint8_t rate)
{
	(void)rate;
#if DAC_EXPLICIT_INPUT
	dac_write(DAC_INPUT_REG, DAC_INPUT_I2S);
#endif
}

void dac_format_dop(uint8_t rate)
{
	(void)rate;
#if DAC_EXPLICIT_INPUT
	dac_write(DAC_INPUT_REG, DAC_INPUT_DSD);
#endif
}

void dac_format_dsd(uint8_t rate)
{
	(void)rate;
#if DAC_EXPLICIT_INPUT
	dac_write(DAC_INPUT_REG, DAC_INPUT_DSD);
#endif
}
