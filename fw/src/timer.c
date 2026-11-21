/* Timer 0 is the firmware's time base. */
#include "ct7601.h"

#define TICK_RELOAD_HIGH 0xfe
#define TICK_RELOAD_LOW  0x0c
#define REG_TICKS        0xac /* bank 0: low byte of the tick count, visible to the host */

__xdata volatile uint16_t ticks;
__xdata volatile uint8_t delay_left;

/* Run on every second tick. The main loop consumes them. */
__xdata volatile uint8_t slow_divider;
__xdata volatile uint8_t slow_flag;     /* set every 21 slow ticks */
__xdata volatile uint16_t countdown;    /* counts down to zero and stays there */
__xdata volatile uint8_t elapsed_a;     /* count up and stop at 255 */
__xdata volatile uint8_t elapsed_b;

void timer0_init(void)
{
	TMOD = (TMOD & 0xf0) | 0x01;
	TH0 = 0xff;
	TL0 = 0x9c;
	ET0 = 1;
	TR0 = 1;
}

void timer0_isr(void) __interrupt(1)
{
	uint8_t bank;

	TH0 = TICK_RELOAD_HIGH;
	TL0 = TICK_RELOAD_LOW;
	if (!(ticks++ & 1)) {
		if (++slow_divider > 20) {
			slow_flag = 1;
			slow_divider = 0;
		}
		if (countdown)
			countdown--;
		if (elapsed_a != 0xff)
			elapsed_a++;
		if (elapsed_b != 0xff)
			elapsed_b++;
		bank = REG[REG_BANK];
		REG[REG_BANK] = BANK_MAIN;
		REG[REG_TICKS] = (uint8_t)ticks;
		REG[REG_BANK] = bank;
	}
	if (delay_left)
		delay_left--;
}

void delay(uint8_t n)
{
	delay_left = n;
	while (delay_left)
		;
}

void delay_long(uint16_t n)
{
	uint8_t i;

	for (i = 0; i < n; i++)
		delay(20);
}
