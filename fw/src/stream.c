/* The stream state machine: follows what the audio path reports and decides
 * when to mute, when to tell the DAC about a new format, and when to unmute.
 * One instance per path; only playback (path 0) carries DSD. */
#include "ct7601.h"
#include "events.h"

__xdata struct stream stream[2];

/* stream_status() bits, from bank 3 (playback) or bank 4 (record) */
#define ST_DATA   0x01 /* register 0x18 bit 3: samples are arriving */
#define ST_DOP    0x02 /* register 0x18 bit 1: DoP markers seen */

/* States. The tens digit is the format: 1x PCM, 2x DoP, 3x DSD. */
#define S_RESET       0x00
#define S_IDLE        0x01
#define S_RESTART     0x02
#define S_WAIT_DATA   0x03
#define S_PCM_FIRST   0x11
#define S_PCM_PROBE   0x13 /* PCM data: give DoP markers time to appear */
#define S_PCM_START   0x14
#define S_PCM_RUN     0x15
#define S_PCM_GAP     0x16
#define S_PCM_MUTED   0x17
#define S_PCM_STOP    0x18
#define S_DOP_START   0x24
#define S_DOP_RUN     0x25
#define S_DOP_GAP     0x26
#define S_DOP_MUTED   0x27
#define S_DOP_STOP    0x28
#define S_DSD_FIRST   0x31
#define S_DSD_WAIT    0x33
#define S_DSD_START   0x34
#define S_DSD_RUN     0x35
#define S_DSD_GAP     0x36
#define S_DSD_STOP    0x38
#define S_STOP_ALL    0x48

#define MODE_PCM 0x10
#define MODE_DOP 0x20
#define MODE_DSD 0x30

__xdata uint8_t stream_format;

static void print_format(void)
{
	static __code const char * __code const pcm[10] = {
		"PPCM44.1K\r\n", "PPCM48K\r\n", "PPCM88.2K\r\n", "PPCM96K\r\n", "PPCM176.4K\r\n",
		"PPCM192K\r\n", "PPCM352.8K\r\n", "PPCM384K\r\n", "PPCM705.6K\r\n", "PPCM768K\r\n",
	};
	static __code const char * __code const dop[4] = {"PDOP64\r\n", "PDOP128\r\n", "PDOP256\r\n", "PDOP512\r\n"};
	static __code const char * __code const dsd[4] = {"PDSD64\r\n", "PDSD128\r\n", "PDSD256\r\n", "PDSD512\r\n"};
	uint8_t n = stream_format & 0x0f;

	if ((stream_format & 0xf0) == FORMAT_PCM && n < 10)
		uart_printf(pcm[n], 0);
	else if ((stream_format & 0xf0) == FORMAT_DOP && n < 4)
		uart_printf(dop[n], 0);
	else if ((stream_format & 0xf0) == FORMAT_DSD && n < 4)
		uart_printf(dsd[n], 0);
}

void stream_format_pcm(uint8_t rate)
{
	stream_format = rate | FORMAT_PCM;
	gpio_set(PIN_DSD, 0);
	print_format();
	dac_format_pcm(rate);
}

void stream_format_dop(uint8_t rate)
{
	stream_format = rate | FORMAT_DOP;
	gpio_set(PIN_DSD, 1);
	print_format();
	dac_format_dop(rate);
}

void stream_format_dsd(uint8_t rate)
{
	stream_format = rate | FORMAT_DSD;
	gpio_set(PIN_DSD, 1);
	print_format();
	dac_format_dsd(rate);
}

static uint8_t stream_status(uint8_t path)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t v = stream[path].status & 0xf0;

	reg_write(REG_BANK, path ? 0x04 : 0x03);
	if (reg_read(0x03) & 0x10)
		v |= 0x08;
	if (reg_read(0x05) & 0x40)
		v |= 0x04;
	if (reg_read(0x18) & 0x02)
		v |= ST_DOP;
	if (reg_read(0x18) & 0x08)
		v |= ST_DATA;
	REG[REG_BANK] = bank;
	return v;
}

/* Switches the path's output stage to PCM, DoP or DSD and unmutes; any other
 * mode only applies its low bit as a mute request. */
static void output_mode(uint8_t path, uint8_t mode)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t clock_bank = path ? 0x09 : 0x07;

	reg_write(REG_BANK, path ? 0x04 : 0x03);
	switch (mode) {
	case MODE_PCM:
		reg_update(0x05, 0x00, 0xc0);
		reg_update(0x03, 0x00, 0x10);
		reg_write(REG_BANK, clock_bank);
		reg_write(0x05, 0x50);
		break;
	case MODE_DOP:
		reg_update(0x05, 0x00, 0xc0);
		reg_update(0x03, 0x10, 0x10);
		reg_write(REG_BANK, clock_bank);
		reg_write(0x05, 0x0a);
		break;
	case MODE_DSD:
		reg_update(0x05, 0xc0, 0xc0);
		reg_update(0x03, 0x10, 0x10);
		reg_write(REG_BANK, clock_bank);
		reg_write(0x05, 0x0a);
		break;
	}
	REG[REG_BANK] = bank;
	path_mute(path, mode);
	if (mode & 0x10)
		path_state |= path ? 0x08 : 0x02;
	else
		path_state &= path ? ~0x08 : ~0x02;
}

/* Back to the start: muted, waiting for data, and with no memory of the rate
 * the PLL was set for, so the next rate request reprograms it in full. */
void stream_reset(uint8_t path)
{
	stream[path].state = S_RESET;
	stream[path].hold = 0;
	stream[path].status = 0xf0;
}

/* True while the path is unmuted and playing. */
uint8_t stream_running(uint8_t path)
{
	uint8_t s = stream[path].state;

	return s == S_PCM_RUN || s == S_DOP_RUN || s == S_DSD_RUN;
}

/* DoP and DSD rates are numbered from the PCM rate index: DoP64 rides on
 * 176.4 kHz (index 4), DSD64 on 88.2 kHz (index 2), doubling every two. The
 * vendor code lets a lower index wrap round to 0x7f or so. */
static uint8_t format_index(uint8_t rate, uint8_t first)
{
#if ROBUST
	if (rate < first)
		return 0;
#endif
	return (uint8_t)(((uint16_t)rate - first) >> 1);
}

static void playback_gate(uint8_t on)
{
	uint8_t bank = REG[REG_BANK];

	reg_write(REG_BANK, 0x03);
	reg_update(0x03, on ? 0x02 : 0x00, 0x02);
	REG[REG_BANK] = bank;
}

void stream_service(uint8_t path)
{
	uint8_t status = stream_status(path);
	uint8_t old = stream[path].state;
	uint8_t state = old;
	uint8_t hold = stream[path].hold;
	uint8_t rate = play_alt >> 4;
	uint8_t pcm_next = (cfg & CFG_DETECT_DOP) ? S_PCM_PROBE : S_PCM_START;

	switch (old) {
	case S_RESET:
		path_mute(path, 1);
		state = S_WAIT_DATA;
		break;
	case S_IDLE:
		state = S_WAIT_DATA;
		break;
	case S_RESTART:
		path_mute(path, 1);
		state = S_WAIT_DATA;
		/* fall through */
	case S_WAIT_DATA:
		if (!(status & ST_DATA))
			break;
		if (hold == 1 || (play_flags[0] & PLAY_DSD)) {
			play_flags[0] |= PLAY_DSD;
			state = S_DSD_START;
		} else {
			state = pcm_next;
		}
		hold = 0;
		elapsed_a = 0;
		play_flags[0] &= ~PLAY_FORMAT_CHANGED;
		break;
	case S_PCM_FIRST:
		if (!(status & ST_DATA))
			break;
		state = pcm_next;
		elapsed_a = 0;
		break;
	case S_PCM_PROBE:
		if ((status >> 4) <= 3)
			state = S_PCM_START;
		else if (elapsed_a > 100)
			state = (status & ST_DOP) ? S_DOP_START : S_PCM_START;
		else if (hold || !(status & ST_DATA))
			state = S_RESTART;
		break;
	case S_PCM_START:
		stream_format_pcm(rate);
		output_mode(path, MODE_PCM);
		state = S_PCM_RUN;
		break;
	case S_DOP_START:
		if (cfg & CFG_DETECT_DOP) {
			stream_format_dop(format_index(rate, 4));
			output_mode(path, MODE_DOP);
		} else {
			output_mode(path, 1);
		}
		state = S_DOP_RUN;
		break;
	case S_PCM_RUN:
		if (hold || (play_flags[0] & PLAY_FORMAT_CHANGED)) {
			state = S_PCM_STOP;
		} else if ((status & ST_DOP) || !(status & ST_DATA)) {
			state = S_PCM_STOP;
			elapsed_a = 0;
		}
		break;
	case S_DOP_RUN:
		if (hold || (play_flags[0] & PLAY_FORMAT_CHANGED)) {
			state = S_DOP_STOP;
		} else if (!(status & ST_DOP) || !(status & ST_DATA)) {
			state = S_DOP_STOP;
			elapsed_a = 0;
		}
		break;
	case S_PCM_GAP:
		if (status & ST_DATA)
			state = S_PCM_RUN;
		else if (elapsed_a > 10)
			state = S_PCM_STOP;
		break;
	case S_DOP_GAP:
		if (status & ST_DATA)
			state = S_DOP_RUN;
		else if (elapsed_a > 1)
			state = S_DOP_STOP;
		break;
	case S_PCM_MUTED:
		path_mute(path, 1);
		if (!(status & ST_DOP))
			state = S_PCM_START;
		else if (elapsed_a > 20)
			state = S_DOP_START;
		break;
	case S_DOP_MUTED:
		path_mute(path, 1);
		if (status & ST_DOP)
			state = S_DOP_START;
		else if (elapsed_a > 120)
			state = S_PCM_START;
		break;
	case S_DSD_FIRST:
		if (status & ST_DATA)
			state = S_DSD_START;
		/* fall through */
	case S_DSD_WAIT:
		if (elapsed_a > 20)
			state = S_DSD_START;
		break;
	case S_DSD_START:
		if (cfg & CFG_DETECT_DOP) {
			stream_format_dsd(format_index(rate, 2));
			output_mode(path, MODE_DSD);
		} else {
			output_mode(path, 1);
		}
		state = S_DSD_RUN;
		break;
	case S_DSD_RUN:
		if (hold || (play_flags[0] & PLAY_FORMAT_CHANGED)) {
			state = S_DSD_STOP;
		} else if (!(status & ST_DATA)) {
			state = S_DSD_STOP;
			elapsed_a = 0;
		}
		break;
	case S_DSD_GAP:
		if (status & ST_DATA)
			state = S_DSD_RUN;
		else if (elapsed_a > 20)
			state = S_DSD_STOP;
		break;
	case S_PCM_STOP:
	case S_DOP_STOP:
	case S_DSD_STOP:
	case S_STOP_ALL:
		path_mute(path, old == S_STOP_ALL ? 0x85 : (old & 0xf0) | 0x01);
		playback_gate(0);
		elapsed_a = 0;
		state = S_IDLE;
		break;
	default:
		state = S_IDLE;
	}
	stream[path].status = status;
	stream[path].hold = hold;
	if (state != old)
		stream[path].state = state;
}
