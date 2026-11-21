# Builds the CT7601 firmware with SDCC: `make` for the robust build, `make
# port` for the one that behaves like the firmware the reference design ships
# with, `make test` for the emulator tests.
all: robust

# Variables go where the shipped firmware kept RAM it no longer needed after
# start-up (its copy of the identity table).
SRC = main main_loop mailbox power hid isr stream audio_path requests system descriptors descriptors_data audio_init control_table usb_phy uart regs timer i2c dac_es9018k2m
CFLAGS = -mmcs51 --model-large --std-c11 --opt-code-size --xram-loc 0x26c0 --xram-size 0x0140 --disable-warning 147 -Ibuild
HEADERS = src/ct7601.h src/descriptors.h src/events.h build/identity.h

# What the device calls itself on USB, e.g.
#   make robust MANUFACTURER="Acme" PRODUCT="Acme DAC"
# Texts are cut or padded to 32 characters: plain ASCII, no quotes or
# backslashes. Ids are four hex digits. The default ids are the reference
# design's.
USB_VID      ?= 2fc6
USB_PID      ?= f801
UAC1_PID     ?= f841
MANUFACTURER ?= openskylark
PRODUCT      ?= CT7601 ES9018K2M
SERIAL       ?= $(PRODUCT)
PLAY_NAME    ?= $(PRODUCT)
RECORD_NAME  ?= $(PRODUCT)
PLAY2_NAME   ?= $(PRODUCT)
UAC1_PRODUCT ?= $(PRODUCT)

define identity_h
id() { printf '#define %s {0x%s, 0x%s}\n' "$$1" "$$(printf %s "$$2" | cut -c1-2)" "$$(printf %s "$$2" | cut -c3-4)"; }; \
tx() { printf '#define %s "%-32.32s"\n' "$$1" "$$2"; }; \
id ID_VID "$(USB_VID)"; id ID_PID "$(USB_PID)"; id ID_UAC1_PID "$(UAC1_PID)"; \
tx ID_MANUFACTURER "$(MANUFACTURER)"; tx ID_PRODUCT "$(PRODUCT)"; tx ID_SERIAL "$(SERIAL)"; \
tx ID_PLAY_NAME "$(PLAY_NAME)"; tx ID_RECORD_NAME "$(RECORD_NAME)"; tx ID_PLAY2_NAME "$(PLAY2_NAME)"; \
tx ID_UAC1_PRODUCT "$(UAC1_PRODUCT)"
endef

# Rewritten only when a value changes, so that changing one rebuilds.
build/identity.h: FORCE
	@mkdir -p build
	@{ $(identity_h); } > $@.tmp
	@cmp -s $@.tmp $@ || cp $@.tmp $@
	@rm -f $@.tmp

FORCE:

# port: behaves exactly like the vendor image; the tests hold it to that.
build/port/%.rel: src/%.c $(HEADERS)
	mkdir -p build/port
	sdcc $(CFLAGS) -DROBUST=0 -c -o $@ $<

build/port/port.bin: $(SRC:%=build/port/%.rel)
	sdcc $(CFLAGS) -o build/port/port.ihx $^
	objcopy -I ihex -O binary build/port/port.ihx $@

port: build/port/port.bin

# robust: the same source with the fixes for the vendor firmware's own
# weaknesses switched on (see README).
build/robust/%.rel: src/%.c $(HEADERS)
	mkdir -p build/robust
	sdcc $(CFLAGS) -DROBUST=1 -c -o $@ $<

build/robust/robust.bin: $(SRC:%=build/robust/%.rel)
	sdcc $(CFLAGS) -o build/robust/robust.ihx $^
	objcopy -I ihex -O binary build/robust/robust.ihx $@

robust: build/robust/robust.bin

# Runs the builds in the emulator. The tests that compare them with the
# shipped firmware need that firmware, which is not in this repository: read
# it from a unit with `ct7601-isp flash read stock.bin` and pass
# STOCK=stock.bin. Without it those tests are skipped.
test: port robust
ifneq ($(STOCK),)
	head -c 21682 $(STOCK) > build/orig.bin
else
	rm -f build/orig.bin
endif
	go test ./emu/

# An annotated listing of the shipped firmware, made from your own unit's
# flash and notes.txt (which names things in the reference build of Sep 14
# 2021, 21682 bytes long). It is assembled again and compared with the image
# to prove it exact.
listing:
ifeq ($(STOCK),)
	$(error pass STOCK=your-flash-dump.bin)
endif
	mkdir -p build
	head -c 21682 $(STOCK) > build/orig.bin
	go run ./gen -notes notes.txt build/orig.bin > build/stock.asm
	sdas8051 -plosgff build/stock.rel build/stock.asm
	sdld -i build/stock.ihx build/stock.rel
	objcopy -I ihex -O binary build/stock.ihx build/stock.bin
	cmp build/stock.bin build/orig.bin
	@echo build/stock.asm matches the image

clean:
	rm -rf build

.PHONY: all port robust test listing clean FORCE
