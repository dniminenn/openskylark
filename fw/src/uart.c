/* Debug output on the serial port, 8N1 from timer 2. */
#include "ct7601.h"

void uart_init(void)
{
	ES = 0;
	T2CON = 0x34;
	SCON = 0x50;
	PCON &= 0xbf;
	PCON |= 0x40;
	RCAP2H = 0xff;
	RCAP2L = 0x98;
	TH2 = RCAP2H;
	TL2 = RCAP2L;
	ES = 1;
}

/* Interrupts are off for the length of a character, as in the vendor code.
 * With the receiver and the serial interrupt switched off instead, the
 * transmit flag never came up on a real CT7601 and the first character
 * hung the firmware, so the set-up above stays exactly the vendor's. The
 * robust build also stops waiting after about a millisecond. */
void uart_putc(uint8_t c)
{
#if ROBUST
	uint8_t wait = 0;

	EA = 0;
	SBUF = c;
	while (!TI && --wait)
		;
	TI = 0;
	EA = 1;
#else
	EA = 0;
	SBUF = c;
	while (!TI)
		;
	TI = 0;
	EA = 1;
#endif
}

static uint8_t hex_digit(uint8_t n)
{
	return n < 10 ? '0' + n : 'A' - 10 + n;
}

void uart_puthex(uint8_t v)
{
	uart_putc(hex_digit(v >> 4));
	uart_putc(hex_digit(v & 0x0f));
}

void uart_putdec(uint8_t v)
{
	uint8_t hundreds = v / 100;
	uint8_t tens = (v - hundreds * 100) / 10;

	if (v >= 100)
		uart_putc('0' + hundreds);
	if (v >= 10)
		uart_putc('0' + tens);
	uart_putc('0' + v - hundreds * 100 - tens * 10);
}

/* Knows %d, %i, %x and %X, all taking the one byte argument. */
void uart_printf(const char __code *format, uint8_t arg)
{
	uint8_t c;

	for (; (c = *format) != 0; format++) {
		if (c != '%') {
			uart_putc(c);
			continue;
		}
		c = *++format;
		if (c == 'd' || c == 'i')
			uart_putdec(arg);
		else if (c == 'x' || c == 'X')
			uart_puthex(arg);
	}
}

/* The receiver is enabled and its interrupt unmasked, so the flag has to be
 * cleared or the handler would be re-entered for ever. Nothing reads RX. The
 * robust build clears the transmit flag as well, in case a character
 * finishes after uart_putc() has stopped waiting for it. */
void serial_isr(void) __interrupt(4)
{
	if (RI)
		RI = 0;
#if ROBUST
	if (TI)
		TI = 0;
#endif
}
