/* Sample rate, format and mute for the two audio paths. Path 0 is playback
 * (bank 3), path 1 is record (bank 4). */
#include "ct7601.h"
#include "events.h"

#define CURRENT_HZ ((volatile uint8_t __xdata *)0x2000) /* little-endian, written by the host's request */

__xdata uint8_t path_state;   /* PATH_PLAY_MUTED, PATH_REC_MUTED */
__xdata uint8_t path_bits;    /* low two bits: playback sample bytes */
__xdata uint8_t play_flags[2];
__xdata uint8_t rec_flags[2];

/* By alternate setting: low nibble channels, bits 6:4 bytes per sample,
 * bit 7 DSD. */
static __code const uint8_t alt_format[] = {
	0x22, 0x22, 0x32, 0x42, 0xc2, 0x22, 0x22, 0x32, 0x42, 0xc2, 0x22, 0x22, 0x32, 0x42, 0xc2,
};

/* By rate index: samples per 10 ms. */
static __code const uint16_t rate_div100[11] = {
	441, 480, 882, 960, 1764, 1920, 3528, 3840, 7056, 7680, 320,
};

/* Bank 3 register 0x27 by rate index. The original has ten entries and reads
 * one byte past them for 32 kHz. */
static __code const uint8_t rate_reg27[11] = {
	0x30, 0x32, 0x38, 0x3a, 0x3c, 0x3e, 0x3e, 0x3e, 0x3e, 0x3e, 0x90,
};

/* 0-9 for 44.1 kHz to 768 kHz, 10 for 32 kHz, 1 for anything else. */
uint8_t rate_lookup(uint32_t hz)
{
	uint8_t i;

	for (i = 0; i < 10; i++)
		if (hz == ((i & 1) ? 48000UL : 44100UL) << (i >> 1))
			return i;
	return hz == 32000UL ? 10 : 1;
}

void path_mute(uint8_t path, uint8_t on)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t forced;

	if (path == 0) {
		reg_write(REG_BANK, 0x03);
		forced = (((cfg & 0x04) ? rec_flags[0] : play_flags[0]) >> 4) & 1;
	} else {
		reg_write(REG_BANK, 0x04);
		forced = (rec_flags[0] >> 4) & 1;
	}
	if ((on & 1) || forced) {
		reg_update(0x01, 0x01, 0x01);
		reg_update(0x1e, 0x08, 0x08);
		reg_update(0x21, 0x80, 0x80);
		dac_mute(1);
		path_state |= path ? PATH_REC_MUTED : PATH_PLAY_MUTED;
	} else {
		reg_update(0x01, 0x00, 0x01);
		reg_update(0x1e, 0x00, 0x08);
		reg_update(0x21, 0x00, 0x80);
		path_state &= path ? ~PATH_REC_MUTED : ~PATH_PLAY_MUTED;
		dac_mute(0);
	}
	REG[REG_BANK] = bank;
}

void path_set_rate(uint8_t path, uint8_t rate)
{
	uint8_t bank = REG[REG_BANK];
	uint32_t nco;
	uint8_t i;

	if (path == 0 || path == 0x10) {
		reg_write(REG_BANK, 0x08);
		if (rate < 10)
			nco = ((rate & 1) ? 0x2ee00000UL : 0x33053978UL) >> (rate >> 1);
		else
			nco = 0x46500000UL;
		for (i = 0; i < 4; i++) {
			reg_write(0x08 + i, (uint8_t)nco);
			nco >>= 8;
		}
		reg_write(0x06, (uint8_t)rate_div100[rate]);
		reg_write(0x07, (uint8_t)(rate_div100[rate] >> 8));
		/* The rate the PLL is set for is kept in the high nibble of the
		 * playback status byte; the state machine's DoP probe reads it
		 * from there. */
		if ((stream[0].status >> 4) != rate) {
			reg_update(0x0c, 0x80, 0x80);
			stream[0].status = (stream[0].status & 0x0f) | (rate << 4);
		}
		reg_write(REG_BANK, 0x03);
		reg_write(0x27, rate_reg27[rate]);
	}
	REG[REG_BANK] = bank;
}

void path_set_format(uint8_t bits, uint8_t channels, uint8_t endpoint)
{
	uint8_t bank = REG[REG_BANK];
	uint16_t window;
	uint8_t level;

	reg_write(REG_BANK, 0x07);
	reg_write(0x05, 0x02);
	reg_update(0x0a, endpoint << 4, 0xf0);
	reg_update(0x40, endpoint << 4, 0xf0);
	reg_write(0x02, 0x00);
	reg_write(0x01, 0x00);
	reg_update(0x07, 4 - (bits >> 3), 0x03);
	reg_update(0x07, channels << 4, 0xf0);
	if (channels == 6) {
		window = 0x01f8;
		level = 0x23;
	} else {
		window = 0x01f8;
		level = 0x17;
	}
	window--;
	reg_write(0x04, (uint8_t)(window >> 8));
	reg_write(0x03, (uint8_t)window);
	reg_update(0x08, level, 0x3f);
	reg_write(0x05, (cfg & CFG_UAC1) ? 0x50 : 0x0a);
	reg_write(REG_BANK, 0x03);
	reg_update(0x1e, channels - 1, 0x07);
	REG[REG_BANK] = bank;
}

#if ROBUST
/* The playback buffer holds 504 channel samples and should run about half
 * full. The feedback the chip gives the host follows its clock, not the
 * level, so a level knocked off centre (by a host that started sending
 * while the feedback still described the previous rate, or by lost packets)
 * stays there, and past the ends samples are dropped or repeated without
 * end. Resetting the path refills the buffer to the middle. */
#define LEVEL_LOW       0x1d /* bank 7; read just before each packet lands */
#define LEVEL_HIGH      0x1e /* bit 0 */
#define PATH_CONTROL    0x05
#define PATH_RESET      0x02
#define COUNT_AT_START  0x95 /* bank 0, for the host */
#define COUNT_PLAYING   0x96

/* Upper limit less 256 by rate index / 2: full reads 504 less one packet. */
static __code const uint8_t level_limit[8] = {0xbc, 0xb0, 0x98, 0x68, 0x20, 0xbc, 0xbc, 0xbc};

static void playback_recentre(uint8_t counter)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t mode;

	REG[REG_BANK] = BANK_MAIN;
	REG[counter]++;
	REG[REG_BANK] = 0x07;
	mode = REG[PATH_CONTROL];
	REG[PATH_CONTROL] = PATH_RESET;
	delay(2);
	REG[PATH_CONTROL] = mode;
	REG[REG_BANK] = bank;
}

/* True if the level read out of range eight times running. It moves while
 * it is read, so the low byte is taken either side of the high bit. */
static uint8_t playback_level_bad(void)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t limit = level_limit[(play_alt >> 5) & 7];
	uint8_t n, low, low2, high, wait;

	REG[REG_BANK] = 0x07;
	for (n = 0; n < 8; n++) {
		low = REG[LEVEL_LOW];
		high = REG[LEVEL_HIGH] & 1;
		low2 = REG[LEVEL_LOW];
		if (high ? (low < limit || low2 < limit) : (low >= 72 || low2 >= 72))
			break;
		for (wait = 0x80; wait; wait--)
			;
	}
	REG[REG_BANK] = bank;
	return n == 8;
}

/* Called while still muted, once the unmute delay has let the feedback
 * settle. At 352.8 kHz and up a reset now and then comes back full. */
void playback_settle(void)
{
	uint8_t tries;

	for (tries = 0; tries < 4; tries++) {
		playback_recentre(COUNT_AT_START);
		delay(20);
		if (!playback_level_bad())
			break;
	}
}

/* Called from the main loop every slow tick. */
void playback_level_watch(void)
{
	if (stream_running(0) && playback_level_bad())
		playback_recentre(COUNT_PLAYING);
}
#endif

static __xdata uint8_t last_format; /* the original keeps this between calls too */

/* Configures the playback path for an alternate setting at the frequency
 * the host last requested. */
static void playback_apply(uint8_t alt)
{
	uint8_t rate, bits, channels, dsd;

	rate = rate_lookup((uint32_t)CURRENT_HZ[0] | ((uint32_t)CURRENT_HZ[1] << 8) |
			   ((uint32_t)CURRENT_HZ[2] << 16) | ((uint32_t)CURRENT_HZ[3] << 24));
	last_format = alt_format[alt];
	channels = last_format & 0x0f;
	bits = (last_format & 0x70) >> 1;
	dsd = (last_format & 0x80) ? 1 : 0;
	if (cfg & CFG_UAC1) {
		if (bits == 0x20)
			bits = 0x10;
	} else if (cfg & 0x20) {
		bits = 0x20;
	}
	if (!(cfg & 0x04)) {
		path_mute(0, 1);
		path_set_rate(0, rate);
		path_set_format(bits, channels, 1);
		path_bits = (path_bits & 0xfc) | (bits >> 4);
		play_flags[0] |= PLAY_FORMAT_CHANGED;
		play_flags[0] &= ~PLAY_DSD;
		if (dsd)
			play_flags[0] |= PLAY_DSD;
	}
	play_alt = (rate << 4) | alt;
}

/* The host selected an alternate setting or set the sampling frequency.
 * which is 1 for playback; 2 (record) only logs. */
void interface_event(uint8_t which)
{
#if ROBUST
	uint8_t alt, seq, again;
#endif

	if (which != 1) {
		uart_printf("xx:[0]\r\n", 0);
		return;
	}
#if ROBUST
	/* Apply what the handler recorded, and again if the host changed it
	 * while that was in progress. */
	do {
		EA = 0;
		alt = alt_pending;
		seq = alt_seq;
		EA = 1;
		playback_apply(alt);
		EA = 0;
		again = (seq != alt_seq);
		if (!again)
			events_a &= ~EV_PLAY_ALT; /* raised for a setting that has just been applied */
		EA = 1;
	} while (again);
#else
	playback_apply(play_alt & 0x0f);
#endif
}
