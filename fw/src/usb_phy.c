/* Getting onto the bus. Register meanings here are inferred from use. */
#include "ct7601.h"

#define USB_CTL       0x30 /* bank 0: bits 1:0 mode, bit 1 doubles as attach */
#define USB_STATUS    0x32 /* bank 0: bit 4 set once high speed is agreed */
#define PHY_BANK      0x0f
#define PHY_TUNE      0x23 /* bits 6:5 */
#define SPEED_WANTED  0x9d /* written without choosing a bank, as the original does */
#define SPEED_RETRIES 0x9c

__xdata uint8_t usb_flags;
#if ROBUST
__xdata uint8_t usb_high_speed_capable;
#endif

void usb_mode(uint8_t mode)
{
	uint8_t bank = REG[REG_BANK];

	reg_write(REG_BANK, BANK_MAIN);
	reg_update(USB_CTL, mode, 0x03);
	REG[REG_BANK] = bank;
}

static void usb_attach(uint8_t on)
{
	uint8_t bank = REG[REG_BANK];

	reg_write(REG_BANK, BANK_MAIN);
	reg_update(USB_CTL, on << 1, 0x02);
	REG[REG_BANK] = bank;
}

static uint8_t usb_is_high_speed(void)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t v;

	reg_write(REG_BANK, BANK_MAIN);
	v = reg_read(USB_STATUS);
	REG[REG_BANK] = bank;
	return v & 0x10;
}

static void phy_tune(uint8_t setting)
{
	uint8_t bank = REG[REG_BANK];

	reg_write(REG_BANK, PHY_BANK);
	reg_update(PHY_TUNE, setting << 5, 0x60);
	REG[REG_BANK] = bank;
}

/* If the host has not agreed high speed, re-attach with each PHY setting in
 * turn until it does. The vendor code allows 200 ms before the first
 * re-attach and leaves the PHY at whatever setting it ended on; the robust
 * build waits 3 s, starts from the default and goes back to it if nothing
 * helped. */
static void usb_negotiate_speed(void)
{
	uint8_t tries = 0;
#if ROBUST
	uint8_t bank = REG[REG_BANK];

	phy_tune(0);
	for (tries = 0; tries < 150 && !usb_is_high_speed(); tries++)
		delay(40);
	tries = 0;
	/* The vendor code writes its two debug bytes with whatever bank
	 * start-up left selected, which is 6. Kept there: it is the only
	 * arrangement that has run on hardware. */
	REG[REG_BANK] = 0x06;
#else
	delay_long(20);
#endif
	REG[SPEED_WANTED] = (cfg >> 1) & 1;
	if (!usb_is_high_speed()) {
		for (tries = 1; tries < 4; tries++) {
			usb_attach(0);
			phy_tune(tries);
			delay_long(40);
			usb_attach(1);
			delay_long(40);
			if (usb_is_high_speed())
				break;
		}
	}
#if ROBUST
	if (!usb_is_high_speed())
		phy_tune(0);
	REG[REG_BANK] = 0x06;
	REG[SPEED_RETRIES] = tries;
	REG[REG_BANK] = bank;
#else
	REG[SPEED_RETRIES] = tries;
#endif
}

void usb_enable(void)
{
	uint8_t bank = REG[REG_BANK];

	reg_write(REG_BANK, BANK_MAIN);
	reg_write(0x43, 0x02);
	REG[REG_BANK] = bank;
}

void usb_connect(void)
{
#if ROBUST
	usb_high_speed_capable = cfg & CFG_HIGH_SPEED;
#endif
	usb_mode((cfg & CFG_HIGH_SPEED) ? 3 : 2);
	if (cfg & CFG_HIGH_SPEED)
		usb_negotiate_speed();
	usb_flags |= 0x10;
}
