/* Acting on class requests. The chip has already stored what the host sent
 * in the answer buffers; these read it back and apply it. */
#include "ct7601.h"
#include "events.h"

/* Answer buffers the chip fills in (see control_table.c). */
#define PLAY_MUTE    (*(volatile uint8_t __xdata *)0x21c0)
#define UNIT_0B_MUTE (*(volatile uint8_t __xdata *)0x2260)
#define REC_MUTE     (*(volatile uint8_t __xdata *)0x2300)
#define VOLUME(n)    ((volatile uint8_t __xdata *)(0x21c4 + 16 * (n))) /* master, left, right; little-endian, 1/256 dB */

#if ROBUST
/* A class request reaches the firmware as one cause bit and one record,
 * and a second request before the handler has read the first replaces it.
 * What the host asked for is still in the buffers the chip answers from,
 * so compare those with what was last applied and raise the event the
 * handler could not. Each must differ on two slow ticks running. */
#define CURRENT_HZ ((volatile uint8_t __xdata *)0x2000)

static __xdata uint8_t applied_mute, applied_volume;
static __xdata uint8_t rate_differs, mute_differs, volume_differs;

static uint8_t volume_sum(void)
{
	uint8_t n, sum = 0;

	for (n = 0; n < 3; n++)
		sum = (uint8_t)((sum << 1) | (sum >> 7)) ^ VOLUME(n)[0] ^ (uint8_t)(VOLUME(n)[1] * 3);
	return sum;
}

static uint32_t current_hz(void)
{
	return (uint32_t)CURRENT_HZ[0] | ((uint32_t)CURRENT_HZ[1] << 8) |
	       ((uint32_t)CURRENT_HZ[2] << 16) | ((uint32_t)CURRENT_HZ[3] << 24);
}

void settings_watch(void)
{
	uint32_t hz = current_hz();

	if (hz == current_hz() && rate_lookup(hz) != (play_alt >> 4) && !(cfg & 0x04)) {
		if (++rate_differs >= 2 && !(events_a & (EV_PLAY_CLOCK | EV_PLAY_ALT))) {
			rate_differs = 0;
			ATOMIC(events_a |= EV_PLAY_CLOCK);
		}
	} else {
		rate_differs = 0;
	}
	if ((PLAY_MUTE & 1) != applied_mute) {
		if (++mute_differs >= 2 && !(events_b & EV_PLAY_MUTE)) {
			mute_differs = 0;
			ATOMIC(events_b |= EV_PLAY_MUTE);
		}
	} else {
		mute_differs = 0;
	}
	if (volume_sum() != applied_volume) {
		if (++volume_differs >= 2 && !(events_b & EV_PLAY_VOL_ALL)) {
			volume_differs = 0;
			ATOMIC(events_b |= EV_PLAY_VOL_ALL);
		}
	} else {
		volume_differs = 0;
	}
}
#endif

static void feature_request(uint8_t which)
{
	uint8_t bank = reg_read(REG_BANK);

	switch (which) {
	case 0x00:
		play_flags[0] = (play_flags[0] & ~PLAY_HOST_MUTE) | ((PLAY_MUTE & 1) << 4);
#if ROBUST
		applied_mute = (play_flags[0] >> 4) & 1;
		/* The vendor code unmutes here whenever the host clears its mute,
		 * even with no stream running or while one is being set up. Only
		 * follow the host while audio is actually playing; otherwise the
		 * flag is enough and the state machine applies it when it starts. */
		if ((play_flags[0] & PLAY_HOST_MUTE) || stream_unmuted(0))
			path_mute(0, 0);
#else
		path_mute(0, 0);
#endif
		break;
	case 0x10:
		rec_flags[0] = (rec_flags[0] & ~PLAY_HOST_MUTE) | ((REC_MUTE & 1) << 4);
		path_mute(0, 0);
		break;
	case 0x11:
		rec_flags[0] = (rec_flags[0] & ~PLAY_HOST_MUTE) | ((REC_MUTE & 1) << 4);
		path_mute(1, 0);
		break;
	case 0x20:
		(void)UNIT_0B_MUTE;
		break;
	}
	reg_write(REG_BANK, bank);
}

/* USB volume is a negative number of 1/256 dB; the DAC wants attenuation in
 * half dB, so twice the magnitude with the high byte taken later. */
static uint16_t attenuation(uint16_t db)
{
#if ROBUST
	/* 0x8000 is the USB value for silence; the vendor code turns it into
	 * no attenuation at all. */
	if (db == 0x8000)
		return 0xffff;
	if (db < 0x8000)
		return 0;
#else
	if (db <= 0x8000)
		return 0;
#endif
	return (uint16_t)((0x10000UL - db) << 1);
}

/* Sum of two attenuations as the DAC's register value. The vendor code lets
 * the 16-bit sum wrap, which can turn a very low volume into a loud one. */
static uint8_t combined(uint16_t a, uint16_t b)
{
	uint16_t sum = a + b;

#if ROBUST
	if (sum < a)
		return 0xff;
#endif
	return (uint8_t)(sum >> 8);
}

static uint16_t volume_at(uint8_t n)
{
	return attenuation(VOLUME(n)[0] | ((uint16_t)VOLUME(n)[1] << 8));
}

static void volume_request(uint8_t which)
{
	uint8_t bank = reg_read(REG_BANK);
	uint16_t master;

	if (which == 0) {
#if ROBUST
		applied_volume = volume_sum();
#endif
		master = volume_at(0);
		volume_left = combined(master, volume_at(1));
		volume_right = combined(master, volume_at(2));
		dac_set_volume(volume_left, volume_right);
	}
	reg_write(REG_BANK, bank);
}

void request_event(uint8_t event)
{
	uint8_t record = (cfg & 0x04) ? 0x10 : 0x11;

	switch (event) {
	case EV_PLAY_MUTE:
		feature_request(0);
		break;
	case EV_PLAY_VOL_ALL:
	case EV_PLAY_VOL_LR:
	case EV_PLAY_VOL_REST:
		volume_request(0);
		break;
	case EV_UNIT_13_MUTE:
		feature_request(record);
		break;
	case EV_UNIT_13_VOLUME:
		volume_request(record);
		break;
	}
}
