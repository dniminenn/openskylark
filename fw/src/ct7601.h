/* CT7601 hardware access and the firmware's shared state. */
#ifndef CT7601_H
#define CT7601_H

#include <stdint.h>
#include <8052.h>

/* The chip's registers sit in external data space. Register 0 selects which
 * bank the other 255 addresses reach. The USB host reaches the same
 * registers through a bank select of its own. */
#define REG ((volatile __xdata uint8_t *)0x4000)
#define REG_BANK 0x00

#define BANK_MAIN 0x00
#define BANK_GPIO 0x02
#define BANK_I2C  0x0e

/* Bank 2 */
#define GPIO_CONFIG 0x08
#define GPIO_OUT    0x09

/* GPIO_OUT bits as this board wires them. */
#define PIN_DSD      0x01 /* high while the stream is DSD or DoP */
#define PIN_MUTE     0x02 /* high mutes the analogue output */
#define PIN_DAC_UP   0x20 /* raised once the DAC is configured */

/* Feature flags, set up in main. */
#define CFG_UAC1       0x01 /* full-speed UAC1 device instead of UAC2 */
#define CFG_HIGH_SPEED 0x02
#define CFG_DETECT_DOP 0x40 /* tell PCM, DoP and DSD apart and switch the DAC */
extern __xdata uint8_t cfg;
extern __xdata uint8_t chip_variant;
extern __xdata uint8_t rate_mask[2];  /* sample rates this chip may offer */
extern __xdata uint8_t usb_flags;

uint8_t reg_read(uint8_t reg);
void reg_write(uint8_t reg, uint8_t value);
void reg_update(uint8_t reg, uint8_t value, uint8_t mask);
void gpio_set(uint8_t mask, uint8_t on);
void gpio_config_update(uint8_t value, uint8_t mask);
void gpio_0a_update(uint8_t value, uint8_t mask);
void gpio_0f_update(uint8_t value, uint8_t mask);

extern __xdata volatile uint8_t elapsed_a, elapsed_b, slow_flag;

void timer0_init(void);
void timer0_isr(void) __interrupt(1);
void delay(uint8_t ticks);

void delay_long(uint16_t n);
void uart_init(void);
void serial_isr(void) __interrupt(4);

uint8_t chip_variant_read(void);
void irq_init(void);
void descriptors_init(void);
void gpio_init(void);
void pattern_init(void);
void usb_connect(void);
void usb_mode(uint8_t mode);
#if ROBUST
extern __xdata uint8_t usb_high_speed_capable; /* cfg's speed bit as it was at attach */
#endif
void power_service(void);
void mailbox_service(void);
void dac_write(uint8_t reg, uint8_t value);
void aux_service(void);
void buttons_poll(void);
void flags_service(void);
void audio_path_init(void);
void pll_setup(uint8_t mode);
void control_table_init(void);
void endpoint_table_a_init(void);
void endpoint_table_b_init(void);
void rate_table_init(void);
void volume_range_init(void);

void uart_putc(uint8_t c);
void uart_puthex(uint8_t v);
void uart_putdec(uint8_t v);
void uart_printf(const char __code *format, uint8_t arg);

#define PATH_PLAY_MUTED 0x04
#define PATH_REC_MUTED  0x10
#define PLAY_HOST_MUTE      0x10
#define PLAY_FORMAT_CHANGED 0x20
#define PLAY_DSD            0x40
extern __xdata uint8_t path_state, path_bits;
extern __xdata uint8_t play_flags[2], rec_flags[2];

uint8_t rate_lookup(uint32_t hz);
void path_mute(uint8_t path, uint8_t on);
void path_set_rate(uint8_t path, uint8_t rate);
void path_set_format(uint8_t bits, uint8_t channels, uint8_t endpoint);
void playback_settle(void);
void playback_level_watch(void);
void interface_event(uint8_t which);
void request_event(uint8_t event);
struct stream {
	uint8_t state;
	uint8_t status; /* what stream_status() last returned */
	uint8_t hold;   /* non-zero forces a restart */
};
extern __xdata struct stream stream[2];
void stream_service(uint8_t path);
uint8_t stream_running(uint8_t path);
#if ROBUST
uint8_t stream_unmuted(uint8_t path);
void playback_gap_over(void);
#endif
void stream_reset(uint8_t path);
void stream_format_pcm(uint8_t rate);
void stream_format_dop(uint8_t rate);
void stream_format_dsd(uint8_t rate);

void i2c_init(void);
void i2c_start(void);
void i2c_stop(void);
__bit i2c_send(uint8_t value);
#if ROBUST
extern volatile __bit i2c_timed_out;
#endif
uint8_t i2c_recv(__bit last);

/* Stream format: sample-rate index in the low nibble plus one of these. */
#define FORMAT_PCM 0x10
#define FORMAT_DOP 0x20
#define FORMAT_DSD 0x40
extern __xdata uint8_t stream_format; /* 0 while nothing is playing */
extern __xdata uint8_t volume_left, volume_right;

void dac_init(void);
void dac_configure(void);
void dac_restore(void);
void settings_watch(void);
#if ROBUST
/* What the firmware has seen, at a fixed address so that a host can read it
 * with host command 0xd0 whatever the build. The counters wrap at 256. */
#define TELEMETRY_ADDR  0x27f0
#define TELEMETRY_MAGIC 0x54
struct telemetry {
	uint8_t magic;          /* TELEMETRY_MAGIC once main() has run */
	uint8_t version;        /* 1 */
	uint8_t at_start;       /* buffer resets before unmuting */
	uint8_t playing;        /* buffer resets while playing */
	uint8_t data_lost;      /* times a playing stream was stopped because its data stopped */
	uint8_t starts;         /* times a stream was started */
	uint8_t level_min;      /* lowest and highest buffer level, halved, over the */
	uint8_t level_max;      /* last second; 0xff and 0 when nothing was playing */
	uint8_t records_lost;
	uint8_t dac_faults;
	uint8_t bus_resets;
	uint8_t suspends;
	uint8_t rate_changes;   /* sampling-frequency requests handled */
	uint8_t format_changes; /* alternate-setting changes handled */
	uint8_t state;          /* playback stream state */
	uint8_t rate_alt;       /* rate index << 4 | alternate setting */
};
extern __xdata __at(TELEMETRY_ADDR) struct telemetry telemetry;
extern __xdata uint8_t dac_faults;
#endif
#if ROBUST
extern __xdata uint8_t loop_mask_age;
#endif
void irq_poll(void);
void dac_set_volume(uint8_t left, uint8_t right);
void dac_mute(uint8_t on);
void dac_format_pcm(uint8_t rate);
void dac_format_dop(uint8_t rate);
void dac_format_dsd(uint8_t rate);

#endif
