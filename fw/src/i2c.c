/* The bridge's I2C master, bank 0x0e. Callers select the bank. */
#include "ct7601.h"

#define I2C_CTL    0x10
#define I2C_CONFIG 0x11
#define I2C_TX     0x12
#define I2C_RX     0x13
#define I2C_CMD    0x14

#define CTL_START   0xc0
#define CTL_STOP    0xa0
#define CTL_RX_ACK  0x80
#define CTL_RX_LAST 0x90
#define CTL_DONE    0x01
#define CTL_NO_ACK  0x08
#define CMD_SEND    0x01
#define CMD_RECV    0x03

#define I2C_TIMEOUT 1000

void i2c_init(void)
{
	uint8_t bank = reg_read(REG_BANK);

	reg_write(REG_BANK, BANK_I2C);
	gpio_config_update(0, 0x18);
	reg_write(I2C_CONFIG, 4);
	reg_write(REG_BANK, bank);
}

#if ROBUST
volatile __bit i2c_timed_out; /* since the last start, a byte never finished: nothing is driving the bus */
#endif

void i2c_start(void)
{
#if ROBUST
	i2c_timed_out = 0;
#endif
	reg_write(I2C_CTL, CTL_START);
}

void i2c_stop(void)
{
	uint8_t i;

	reg_write(I2C_CTL, CTL_STOP);
	/* The master is not polled for the end of the stop condition; this wait
	 * is all that separates it from the next start. 1700 machine cycles, as
	 * in the vendor firmware. */
	for (i = 0; i < 100; i++)
		__asm__("nop\nnop\nnop\nnop\nnop\nnop\nnop\nnop\nnop\nnop\nnop\nnop\nnop\nnop\nnop");
}

/* Returns 1 once the master reports the byte done, 0 on timeout. */
static __bit i2c_wait(void)
{
	uint16_t n;

	for (n = 0; n < I2C_TIMEOUT; n++)
		if (reg_read(I2C_CTL) & CTL_DONE)
			return 1;
	return 0;
}

/* Returns 1 when the device acknowledged. The vendor code ignores the
 * timeout, so a stuck bus reads as an acknowledge there. */
__bit i2c_send(uint8_t value)
{
	__bit done;

	reg_write(I2C_CMD, CMD_SEND);
	reg_write(I2C_TX, value);
	done = i2c_wait();
#if ROBUST
	if (!done) {
		i2c_timed_out = 1;
		return 0;
	}
#else
	(void)done;
#endif
	return !(reg_read(I2C_CTL) & CTL_NO_ACK);
}

uint8_t i2c_recv(__bit last)
{
	reg_write(I2C_CTL, last ? CTL_RX_LAST : CTL_RX_ACK);
	reg_write(I2C_CMD, CMD_RECV);
#if ROBUST
	if (!i2c_wait())
		i2c_timed_out = 1;
#else
	i2c_wait();
#endif
	return reg_read(I2C_RX);
}
