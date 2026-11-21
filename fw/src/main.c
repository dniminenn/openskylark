/* Start-up order follows the vendor firmware. Nothing here relies on
 * initialised XDATA: SDCC would copy it with MOVX @Ri paged through P2, which
 * the vendor code never uses on this chip. */
#include "ct7601.h"
#include "events.h"

void usb_enable(void);
extern __xdata uint8_t power_state;
void main_loop(void);

#if ROBUST
__xdata __at(TELEMETRY_ADDR) struct telemetry telemetry;
#endif

void main(void)
{
#if ROBUST
	uint8_t i;

	/* Outside the RAM the start-up code clears. */
	for (i = 0; i < sizeof telemetry; i++)
		((uint8_t __xdata *)&telemetry)[i] = 0;
	telemetry.magic = TELEMETRY_MAGIC;
	telemetry.version = 1;
	telemetry.level_min = 0xff;
#endif
	chip_variant = chip_variant_read();
	irq_init();
	reg_write(REG_BANK, 0x09);
	reg_write(0x6c, 0x01);
	reg_write(REG_BANK, BANK_MAIN);
	reg_write(0x9e, 0x01); /* firmware version 1.91, readable by the host */
	reg_write(0x9f, 0x91);
	power_state = 1;
#if ROBUST
	stream_reset(0); /* the vendor code starts with "PLL set for 44.1 kHz" although it is not */
#endif
	rate_mask[0] = 0x0d; /* 44.1 kHz to 384 kHz: bits 1, 2, 4, 5, 7, 8, 10, 11 */
	rate_mask[1] = 0xb6;
	cfg = (cfg & 0x5e) | CFG_HIGH_SPEED | 0x40;
	descriptors_init();
	gpio_init();
	audio_path_init();
	i2c_init();
	pattern_init();
	dac_init();
	usb_enable();
	usb_connect();
	main_loop();
}
