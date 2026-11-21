/* Start-up of the pieces around the audio path. */
#include "ct7601.h"

__xdata uint8_t cfg;
__xdata uint8_t chip_variant;
__xdata uint8_t rate_mask[2]; /* main() loads RATE_MASK_DEFAULT */

/* Bank 0x0b receives 8-byte frames into this buffer. What sends them is not
 * known; nothing does on this board. */
static __xdata __at(0x2680) uint8_t aux_frame[8];

/* Patterns the hardware plays out while a DSD stream is muted or changing.
 * Bank 6 holds their address in 4-byte units. */
#define PATTERN_ADDR 0x2688
static __xdata __at(PATTERN_ADDR) uint8_t patterns[0x38];
static __code const uint8_t pattern_data[0x38] = {
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa,
	0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee, 0xee,
	0xfe, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0x7f, 0xbf, 0xdf, 0xef, 0xf7, 0xfb, 0xfd, 0xfc,
	0x7e, 0xbf, 0xdf, 0xef, 0xf7, 0xfb, 0xfd, 0x7e,
	0xff, 0xff, 0xff,
};

/* Bank 2 register 0x13 bits 5:4 tell the CT7601 variants apart. */
uint8_t chip_variant_read(void)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t v;

	reg_write(REG_BANK, BANK_GPIO);
	v = reg_read(0x13);
	REG[REG_BANK] = bank;
	return v;
}

void irq_init(void)
{
	EA = 0;
	IT0 = 1;
	IE0 = 0;
	EX0 = 1;
	IT1 = 1;
	IE1 = 0;
	EX1 = 0;
	timer0_init();
	uart_init();
	REG[REG_BANK] = BANK_MAIN;
	REG[0x1c] = 0x00;
	REG[0x1d] = 0xda;
	REG[0x1e] = 0xff;
	EA = 1;
}

static void aux_rx_init(void)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t i;

	reg_write(REG_BANK, 0x0b);
	reg_write(0x01, 0x56);
	reg_update(0x02, 0x01, 0x01);
	reg_write(0x05, 0x09);
	reg_write(0x04, 0xa0);
	for (i = 0; i < sizeof aux_frame; i++)
		aux_frame[i] = 0;
	reg_write(0x03, sizeof aux_frame);
	reg_write(0x06, sizeof aux_frame);
	reg_write(REG_BANK, bank);
}

void gpio_init(void)
{
	aux_rx_init();
#if ROBUST
	/* The vendor code configures the pins and drives all of them low,
	 * the mute pin included, before raising it, while the DAC is still at
	 * its power-on defaults: unmuted, full volume. Set mute first and
	 * never clear it here. */
	gpio_set(PIN_MUTE, 1);
	gpio_config_update(0x27, 0xff);
	gpio_0a_update(0x00, 0xff);
	gpio_0f_update(0x00, 0xff);
	gpio_set(0x27 & ~PIN_MUTE, 0);
	gpio_set(PIN_MUTE, 1);
#else
	gpio_config_update(0x27, 0xff);
	gpio_0a_update(0x00, 0xff);
	gpio_0f_update(0x00, 0xff);
	gpio_set(0x27, 0);
	gpio_set(PIN_DAC_UP, 0);
	gpio_set(PIN_MUTE, 1);
#endif
}

void pattern_init(void)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t i;

	for (i = 0; i < sizeof patterns; i++)
		patterns[i] = pattern_data[i];
	reg_write(REG_BANK, 0x06);
	reg_write(0xec, (uint8_t)(PATTERN_ADDR >> 2));
	reg_write(0xed, (uint8_t)(PATTERN_ADDR >> 10));
	reg_write(0xee, (uint8_t)((PATTERN_ADDR >> 2) + 0x0d));
	reg_write(0xef, (uint8_t)(((PATTERN_ADDR >> 2) + 0x0d) >> 8));
	REG[REG_BANK] = bank;
}
