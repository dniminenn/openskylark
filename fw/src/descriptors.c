/* Builds the USB descriptors in RAM. The chip answers GET_DESCRIPTOR itself:
 * bank 6 holds each descriptor's address in 4-byte units. */
#include "ct7601.h"
#include "identity.h"
#include "descriptors.h"

#define DESC_START 0x0600 /* x4 = 0x1800 */
#define OTHER_SPEED_AT 0x07c0 /* x4 = 0x1f00 */
#define RAM_END 0x3000

/* bank 6 */
#define PTR_DEVICE      0x10
#define PTR_CONFIG      0x12
#define PTR_CONFIG_2    0x14
#define PTR_STRING      0x18 /* two registers per string */
#define PTR_HID         0x08
#define PTR_HID_REPORT  0x0a
#define LEN_HID_REPORT  0x0c
#define PTR_QUALIFIER   0xa2
#define PTR_OTHER_SPEED 0xa4
#define PTR_OTHER_2     0xa6

/* Identity the board vendor customises: the block the flashing tools find by
 * its "CTUSB_Table01" marker. The vendor firmware copies it to RAM at 0x2682
 * and reads it there; this reads it in place. */
struct identity {
	char marker[13];
	uint8_t vid[2]; /* big-endian */
	uint8_t pid[2];
	char strings[6][32]; /* manufacturer, product, serial, play, record, second play; space padded */
	uint8_t uac1_pid[2];
	char uac1_product[32];
};
/* Every text field is exactly 32 characters, padded with spaces; the
 * terminating NUL of each literal is deliberately dropped. The values come
 * from the Makefile's identity variables through build/identity.h, and
 * `ct7601-isp image set` can change them in a built image. */
static __code const struct identity identity = {
	{'C', 'T', 'U', 'S', 'B', '_', 'T', 'a', 'b', 'l', 'e', '0', '1'},
	ID_VID,
	ID_PID,
	{
		ID_MANUFACTURER,
		ID_PRODUCT,
		ID_SERIAL,
		ID_PLAY_NAME,
		ID_RECORD_NAME,
		ID_PLAY2_NAME,
	},
	ID_UAC1_PID,
	ID_UAC1_PRODUCT,
};

static __code const char hid_name[] = "HID Interface";
static __code const char iap_name[] = "iAP Interface";

static __xdata uint16_t next; /* next free slot, in 4-byte units */

static void store(uint16_t addr, uint8_t value)
{
	if (addr < RAM_END)
		*(uint8_t __xdata *)addr = value;
}

static void put(uint16_t slot, const uint8_t __code *src, uint16_t len)
{
	uint16_t i;

	for (i = 0; i < len; i++)
		store((slot << 2) + i, src[i]);
}

static void advance(uint16_t len)
{
	next += (len + 3) >> 2;
}

/* Writes a descriptor address into a register pair and marks it valid. */
static void set_pointer(uint8_t reg, uint16_t slot)
{
	reg_write(reg, (uint8_t)slot);
	reg_write(reg + 1, (uint8_t)(slot >> 8));
	reg_update(reg + 1, 0x10, 0x30);
}

static void device(uint8_t uac1)
{
	const uint8_t __code *src = uac1 ? desc_device_uac1 : desc_device_uac2;
	uint16_t at = next << 2;

	put(next, src, src[0]);
	store(at + 8, identity.vid[1]);
	store(at + 9, identity.vid[0]);
	if (uac1) {
		store(at + 10, identity.uac1_pid[1]);
		store(at + 11, identity.uac1_pid[0]);
	} else {
		store(at + 10, identity.pid[1]);
		store(at + 11, identity.pid[0]);
	}
	reg_write(REG_BANK, 0x06);
	set_pointer(PTR_DEVICE, next);
	advance(src[0]);
}

static void hid(void)
{
	put(next, desc_hid, desc_hid[0]);
	reg_write(REG_BANK, 0x06);
	reg_write(0x06, 0x02);
	set_pointer(PTR_HID, next);
	advance(desc_hid[0]);

	put(next, desc_hid_report, sizeof desc_hid_report);
	set_pointer(PTR_HID_REPORT, next);
	reg_write(LEN_HID_REPORT, sizeof desc_hid_report);
	reg_write(LEN_HID_REPORT + 1, 0);
	advance(sizeof desc_hid_report);
}

static void qualifier(void)
{
	put(next, desc_qualifier, desc_qualifier[0]);
	reg_write(REG_BANK, 0x06);
	set_pointer(PTR_QUALIFIER, next);
	advance(desc_qualifier[0]);
}

/* Emits one UTF-16 string descriptor from n characters. */
static void string_from(uint8_t index, const char __code *text, uint8_t n)
{
	uint16_t at = next << 2;
	uint8_t i;

	store(at, 2 * n + 2);
	store(at + 1, 0x03);
	for (i = 0; i < n; i++) {
		store(at + 2 + 2 * i, text[i]);
		store(at + 3 + 2 * i, 0);
	}
	reg_write(REG_BANK, 0x06);
	set_pointer(PTR_STRING + 2 * index, next);
	advance(2 * n + 2);
}

static uint8_t trimmed(const char __code *field)
{
	uint8_t n = 32;

	while (n && field[n - 1] == ' ')
		n--;
	return n;
}

static void string(uint8_t index, uint8_t uac1)
{
	const char __code *field;
	uint8_t n;

	switch (index) {
	case 0:
		put(next, desc_langid, desc_langid[0]);
		reg_write(REG_BANK, 0x06);
		set_pointer(PTR_STRING, next);
		advance(desc_langid[0]);
		return;
	case 7:
		for (n = 0; hid_name[n]; n++)
			;
		string_from(index, hid_name, n);
		return;
	case 8:
		for (n = 0; iap_name[n]; n++)
			;
		string_from(index, iap_name, n);
		return;
	}
	if (index == 2 && uac1)
		field = identity.uac1_product;
	else
		field = identity.strings[index - 1];
	string_from(index, field, trimmed(field));
}

static void configuration(void)
{
	const uint8_t __code *src = desc_config_uac2;
	uint16_t len = sizeof desc_config_uac2;

	if (cfg & CFG_UAC1) {
		src = desc_config_uac1;
		len = sizeof desc_config_uac1;
	}
	reg_write(REG_BANK, BANK_MAIN);
	put(next, src, len);
	reg_write(REG_BANK, 0x06);
	set_pointer(PTR_CONFIG, next);
	set_pointer(PTR_CONFIG_2, next);

	reg_write(REG_BANK, BANK_MAIN);
	put(OTHER_SPEED_AT, desc_config_other_speed, sizeof desc_config_other_speed);
	reg_write(REG_BANK, 0x06);
	set_pointer(PTR_OTHER_SPEED, OTHER_SPEED_AT);
	set_pointer(PTR_OTHER_2, OTHER_SPEED_AT);
}

void descriptors_init(void)
{
	uint8_t bank = REG[REG_BANK];
	uint8_t uac1 = cfg & CFG_UAC1;
	uint8_t i;

	next = DESC_START;
	device(uac1);
	hid();
	qualifier();
	for (i = 0; i <= 8; i++)
		string(i, uac1);
	configuration();
	REG[REG_BANK] = bank;
}

/* Replaces the device and configuration descriptors with the firmware-update
 * personality and re-attaches so the host sees a different device. */
void update_mode_enter(void)
{
	next = DESC_START;
	put(next, desc_update_device, sizeof desc_update_device);
	reg_write(REG_BANK, 0x06);
	set_pointer(PTR_DEVICE, next);
	advance(sizeof desc_update_device);
	put(next, desc_update_config, sizeof desc_update_config);
	set_pointer(PTR_CONFIG, next);
	set_pointer(PTR_CONFIG_2, next);
	reg_write(REG_BANK, BANK_MAIN);
	reg_update(0x30, 0x00, 0x02);
	delay_long(200);
	reg_update(0x30, 0x02, 0x02);
	uart_printf("-> default!\r\n", 0);
}
