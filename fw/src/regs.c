#include "ct7601.h"

uint8_t reg_read(uint8_t reg)
{
	return REG[reg];
}

void reg_write(uint8_t reg, uint8_t value)
{
	REG[reg] = value;
}

void reg_update(uint8_t reg, uint8_t value, uint8_t mask)
{
	REG[reg] = (REG[reg] & ~mask) | (value & mask);
}

/* The GPIO helpers switch to bank 2 themselves and put the caller's bank back. */
void gpio_set(uint8_t mask, uint8_t on)
{
	uint8_t bank;
	uint8_t out;
#if ROBUST
	/* The interrupt handler changes another bit of this register; keep it
	 * out while the value is read, changed and written back. */
	__bit ea = EA;

	EA = 0;
#endif
	bank = REG[REG_BANK];
	REG[REG_BANK] = BANK_GPIO;
	out = REG[GPIO_OUT] & ~mask;
	if (on)
		out |= mask;
	REG[GPIO_OUT] = out;
	REG[REG_BANK] = bank;
#if ROBUST
	EA = ea;
#endif
}

void gpio_config_update(uint8_t value, uint8_t mask)
{
	uint8_t bank = REG[REG_BANK];

	REG[REG_BANK] = BANK_GPIO;
	REG[GPIO_CONFIG] = (REG[GPIO_CONFIG] & ~mask) | (value & mask);
	REG[REG_BANK] = bank;
}

/* Bank 2 registers 0x0a and 0x0f: per-pin settings whose meaning is not
 * known yet. Start-up clears both. */
void gpio_0a_update(uint8_t value, uint8_t mask)
{
	uint8_t bank = REG[REG_BANK];

	REG[REG_BANK] = BANK_GPIO;
	REG[0x0a] = (REG[0x0a] & ~mask) | (value & mask);
	REG[REG_BANK] = bank;
}

void gpio_0f_update(uint8_t value, uint8_t mask)
{
	uint8_t bank = REG[REG_BANK];

	REG[REG_BANK] = BANK_GPIO;
	REG[0x0f] = (REG[0x0f] & ~mask) | (value & mask);
	REG[REG_BANK] = bank;
}
