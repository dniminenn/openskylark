/* What the interrupt handler tells the main loop. */
#ifndef EVENTS_H
#define EVENTS_H

#include <stdint.h>

/* events_a */
#define EV_BUS_RESET      0x01
#define EV_PLAY_ALT       0x02 /* playback interface changed alternate setting */
#define EV_ENTITY_0C      0x10
#define EV_PLAY_CLOCK     0x20 /* request to clock source 5 */
#define EV_REC_CLOCK      0x40 /* request to clock source 15 */
#define EV_ENTITY_15      0x80

/* events_b: requests to the feature units */
#define EV_PLAY_MUTE      0x01
#define EV_PLAY_VOL_ALL   0x02
#define EV_PLAY_VOL_LR    0x04
#define EV_PLAY_VOL_REST  0x08
#define EV_UNIT_0B_MUTE   0x10
#define EV_UNIT_0B_VOLUME 0x20
#define EV_UNIT_13_MUTE   0x40
#define EV_UNIT_13_VOLUME 0x80

extern __xdata volatile uint8_t events_a, events_b;
extern __xdata volatile uint8_t irq_seen;      /* which interrupt causes fired since the main loop last looked */
extern __xdata volatile uint8_t pending;       /* bit 7: wake requested; bits 0-3: by which source */
extern __xdata volatile uint8_t power_events;  /* bit 0 suspend, bit 1 resume */
extern __xdata volatile uint8_t aux_flags;     /* bit 7: a frame arrived in the aux buffer */
extern __xdata volatile uint8_t play_alt;      /* low nibble: playback alternate setting; high: its rate index */
#if ROBUST
/* The handler's own record of the alternate setting the host last selected.
 * The main loop only reads these. */
extern __xdata volatile uint8_t alt_pending;
extern __xdata volatile uint8_t alt_seq;
extern __xdata volatile uint8_t play_closed;
/* Times a cause was raised and the request record no longer matched it. */
extern __xdata volatile uint8_t records_lost;
#endif
#define ALT_MAX 4

/* For main-loop updates of a byte the interrupt handler also writes. The
 * vendor code does these unprotected and can lose the handler's bit. */
#if ROBUST
#define ATOMIC(statement) do { EA = 0; statement; EA = 1; } while (0)
#else
#define ATOMIC(statement) do { statement; } while (0)
#endif
extern __xdata volatile uint8_t request_entity;

void int0_isr(void) __interrupt(0);

#endif
