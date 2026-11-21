<p align="center"><img src="logo.svg" alt="openskylark" width="240"></p>

# openskylark

Open firmware for USB DACs that pair the Comtrue CT7601 USB audio bridge
with an ESS ES9018K2M, and the Linux tool I flash it with.

- `fw/src` is the firmware, in C for SDCC.
- `ct7601-isp` reads and writes the bridge's firmware flash, shows what the
  bridge is playing, and reaches the DAC chip behind it over I2C.
- `fw/emu` is an 8051 emulator that the tests run the firmware in.

I wrote this for my own DAC, an InnoMaker SKYLARK UAC2 HIFI 384 (USB id
`2fc6:f801`, device release 2.01). It is the only hardware I have tried it
on. Its stock firmware is SXW's `CT7601_ES9018K2M` image with different name
strings, so I call the hardware the reference design. Throughout this file,
"stock firmware" means the firmware my DAC shipped with.

This is not a Comtrue or SXW project, and the stock firmware is not in this
repository. Where a test needs it, you read it from your own DAC.

Writing firmware can leave a DAC unable to start. Read "Flashing safely"
first.

## Why

My DAC played cleanly from a PC. On my Android phone it gave static in one
channel or constant crackle, depending on the plug-in.

I found two defects in the stock firmware: it can lose the host's choice of
sample format, and it can leave its playback buffer sitting full or empty.
Both are described under "What I found". A Linux host
triggers the buffer defect in ordinary playback. I triggered the format
defect with a test tool, at the spacing Linux uses between the two requests
involved. I measured and fixed both on a PC.

With this firmware the phone plays clean. That is by ear; I measured nothing
on the phone, and I did not isolate which of the changes it needed. That the
two defects are what the phone was hitting is my inference from the
symptoms.

## Quick start

    CGO_ENABLED=0 go build -o ct7601-isp .
    sudo ./ct7601-isp info
    sudo ./ct7601-isp flash read stock.bin

`stock.bin` is your DAC's own firmware. Keep it: it is the way back, and
the comparison tests need it. Then build this firmware and write it:

    cd fw && make
    cd ..
    ./ct7601-isp image set -like stock.bin -o mine.bin fw/build/robust/robust.bin
    sudo ./ct7601-isp flash write -yes mine.bin

The `image set -like` step gives the new image your DAC's USB ids and name
strings, so the host sees the same device as before. Without it the device
calls itself "CT7601 ES9018K2M" by "openskylark". To go back:

    sudo ./ct7601-isp flash write -yes stock.bin

Building the firmware needs SDCC (I used 4.6.0) and objcopy. The tool, the
tests and `make listing` need Go. The Android monitor has its own needs,
listed in its section.

## The tool

Every request is a vendor control transfer on endpoint 0 sent through usbfs,
so the tool runs while `snd-usb-audio` stays bound. Apart from the `flash`
commands, which enter ISP mode and reset the chip afterwards, it can be used
while audio plays. To run it without sudo, install `60-ct7601.rules`. The
default device is `2fc6:f801`; `-vid` and `-pid` select another. Run it with
no arguments for the command list.

I have run every device command on my DAC. I have not re-run `i2c scan`
since adding the firmware pause described below.

### What it does to stay out of the firmware's way

- **One copy at a time.** The chip has a single bank select for the host. The
  tool takes an exclusive lock on the device node, so a second copy waits up
  to 5 s for the first and then gives up.
- **I2C is taken from the firmware, not shared with it.** The bridge has one
  I2C master and the firmware drives the DAC through it whenever a stream
  starts or stops or the host changes volume. Before each I2C transaction
  the tool asks the firmware to skip its event and power handling (host
  command 0xd8, see "Protocol") and waits until the firmware has taken the
  command, which it does between passes of its main loop. It lifts the pause
  after the transaction, on error, and on SIGINT or SIGTERM.
- If the firmware does not take the command within 1.5 s the tool refuses to
  use the bus and replaces the waiting command with one that pauses nothing,
  so a firmware that was only busy is not paused when it gets to it.
  `-nopause` skips the pause, for a chip with no firmware running.
- This firmware drops a pause by itself after 2 s. The stock firmware keeps
  one until it is lifted or the firmware restarts.

What I measured on my DAC running this firmware:

- 40 dumps of 26 DAC registers, run alongside a loop that started and
  stopped eight streams, all completed without error, and the firmware
  counted no DAC faults.
- A pause I set by hand and never lifted held a new stream back for about
  2 s, after which it played.

Before the tool had the pause, with stock firmware on the DAC, one register
dump failed with the I2C master not finishing. I had taken it just after the
kernel driver re-attached to the DAC.

The lock covers copies of the tool only. A `flash verify` that I ran while
the Android monitor was reading the same DAC, through an emulator on the
same PC, reported a difference that was not there; two verifies with the
monitor idle matched.

Register access needs no such care: the host does not see the firmware's
bank selection. 333,000 host reads of the bank register and of a register in
the selected bank, taken while streams started and stopped, never returned
anything but the host's own selection.

### Flashing safely

`flash write` saves the whole flash to a file first, rewrites only the 4 KB
sectors that differ, reads each one back, and makes up to three passes over
whatever still differs. If the new image will not verify it writes the old
one back. If that fails too it leaves the chip in ISP mode without resetting
it and says so, so the command can be run again while the DAC stays plugged
in. I have never needed those last two paths, and they have never run, on
hardware or in a test.

Once, a build of mine hung in its main loop on the DAC. The chip still
answered register and flash requests, so I could write a working build over
it. A firmware that fails before it attaches to USB would presumably not be
reachable that way; I have not tried.

I have not tried a blank or damaged flash either. The vendor's Windows tool
also looks for devices `2fc6:6000` and `0ea0:6000`, and SXW ships drivers
named "Firmware Update Device" for both, which suggests the chip has a
recovery mode of its own. Comtrue's application note for its CT7601
evaluation board (the QFN 48 board, version 0.1 of December 2017) lists a
"USB mode select" jumper with three positions: UAC2, UAC1 and "firmware
update mode". So the mode is chosen by a pin on that board. Comtrue's
brief data sheet for the chip (version 1.0, July 2021) says where such
pins are: the SPI clock, data-out, write-protect and chip-select pins to
the flash double as "power on latch selection" pins 0 to 3, "reserved for
internal software". It does not say which pin or level selects what. I have
not opened my DAC to look.

## The firmware

| `make` target in `fw/` | Result |
|---|---|
| (default), `robust` | `build/robust/robust.bin`: this firmware |
| `port` | `build/port/port.bin`: the same source built to behave like the stock firmware |
| `test` | builds both and runs the emulator tests |
| `listing STOCK=../stock.bin` | `build/stock.asm`: an annotated listing of your DAC's stock firmware |

The `port` build exists so the tests can hold the C source to the stock
firmware's behaviour. I have only run it in the emulator, never on a DAC,
and I do not suggest flashing it.

What the firmware calls itself on USB is a build setting:

    make MANUFACTURER="Acme" PRODUCT="Acme DAC"

The variables are `USB_VID`, `USB_PID`, `UAC1_PID`, `MANUFACTURER`,
`PRODUCT`, `SERIAL`, `PLAY_NAME`, `RECORD_NAME`, `PLAY2_NAME` and
`UAC1_PRODUCT`; texts are cut or padded to 32 characters. The defaults are
the stock ids (`2fc6`, `f801`, `f841`), "openskylark" and "CT7601
ES9018K2M". `ct7601-isp image set` changes the same fields in an image that
is already built, and `-like` copies them all from another image. Linux
enables native DSD for every device with vendor id 2fc6
(`QUIRK_FLAG_DSD_RAW` in `sound/usb/quirks.c`), so another vendor id loses
that unless the kernel is told otherwise.

### What it does differently from the stock firmware

Each difference is marked in the source with the `ROBUST` macro.

- It fixes the three defects under "What I found": the lost sample format,
  the playback buffer, and the transceiver setting
  (`TestAlternateSettingRace`, `TestBufferRecentredBeforeUnmute`,
  `TestRecentreRepeatsWhenFull`, `TestLevelWatch`, `TestTrimAfterSlowHost`).
- It tells the DAC whether I2S or DSD is coming (ES9018K2M register 1 = 0x80
  or 0x83). The stock firmware leaves the DAC on auto-detect (0x8c)
  (`TestDACToldItsInput`).
- Interrupt handler: it goes round while a cause is left, up to four times,
  instead of taking one snapshot. It acknowledges a cause before reading the
  single request record, and acts on the record only if it is the kind of
  request the cause says. The main loop also runs it for a cause that raised
  no interrupt edge (`TestInterruptCauseDuringHandler`,
  `TestCauseWithoutEdge`).
- Every 21 ms it compares rate, mute and volume with what the host last
  wrote into the chip's answer buffers, and applies them if the request was
  missed (`TestLostRequestsRecovered` covers rate and mute).
- It updates the bytes it shares with the interrupt handler, and the
  output-pin register, with interrupts masked.
- It checks DAC writes for acknowledges and repeats them. A byte that never
  finishes does not count as acknowledged and is not retried. It reads the
  start-up table back and writes it again if the DAC does not hold it
  (`TestDACInitSurvivesNacks`, `TestDeadI2CBusAtStartup`).
- Muting keeps the other bits of the DAC's mute register as long as two reads
  of it agree, and otherwise uses the value last written
  (`TestMuteKeepsFilterSettings`).
- It does not unmute if a format or rate change arrived during the wait
  before unmuting, and it waits up to 300 ms for the DAC to report lock
  (`TestNoUnmuteAcrossFormatChange`, `TestUnmuteWaitsForLock`).
- A host request to unmute does not open the output while nothing is playing
  (`TestHostUnmuteWhileIdle`).
- It raises the mute pin before the pins are made outputs and does not drive
  it low during start-up (`TestMutePinDuringStartup`).
- Volume: the USB value for silence gives the DAC's greatest attenuation,
  and the sum of master and channel attenuation saturates instead of
  wrapping. No test covers either.
- Suspend and resume: it mutes the DAC first. After resume it attaches at
  the speed the device is capable of rather than the speed the last session
  got, marks the bus active, writes back what each DAC register last held
  and reapplies the path settings (`TestResumeReconfiguresDAC`,
  `TestResumeKeepsHighSpeed`).
- It rechecks the link speed every 21 ms (`TestSpeedSettlesAfterReset`), and
  a bus reset mutes and restarts the stream state machine.
- Debug output cannot block (`TestSerialCannotBlock`), and a host pause
  expires after 2 s (`TestHostPauseExpires`).
- A PCM stream stays unmuted through a gap in its data of up to 100 ms, and
  the buffer level is checked again as soon as the data is back. The stock
  firmware mutes and starts the stream over at the first moment without
  data. The wait ends at once if the host selects alternate setting 0. DoP
  and native DSD streams are stopped as before (`TestGapRiddenOut`; see
  "Gaps in the data").
- Every DAC register other than register 1 is left as the stock firmware
  leaves it (`TestDACRegistersAsVendor`; see "A DAC setting I took back").

### Counters

The firmware keeps 16 bytes of counters in its RAM at 0x27f0, where a host
can read them with host command 0xd0 (see "Protocol"). `ct7601-isp status`
prints them and the Android monitor logs them. Each is one byte. They start
from zero when the firmware starts, and they wrap at 256, apart from the
lost-request and DAC-fault counts, which stop at 255.

| Byte | Meaning |
|---|---|
| 0 | 0x54, which marks the block as present |
| 1 | layout version, 1 |
| 2 | buffer resets before unmuting at a stream start (bank 0 register 0x95 counts the same) |
| 3 | buffer resets while playing (register 0x96 counts the same) |
| 4 | times a playing stream was stopped because its data stopped; an ordinary stop is one of them |
| 5 | stream starts |
| 6, 7 | lowest and highest buffer level, halved, over the last 48 passes of the 21 ms check; 0xff and 0 when nothing played |
| 8 | requests whose record was lost |
| 9 | DAC writes that were never acknowledged, and failed verifies |
| 10 | bus resets |
| 11 | suspends |
| 12 | sampling-frequency changes handled |
| 13 | alternate-setting changes handled |
| 14 | state of the playback state machine (0x15 while PCM plays) |
| 15 | rate index in the high four bits, alternate setting in the low four |

Bank 0 register 0x98 counts the gaps in a playing stream's data that the
firmware stayed unmuted through; it wraps at 256 too.

`TestTelemetry` takes a stream through starting, starving, stopping,
restarting and a bus reset in the emulator and checks the block against
what happened. It does not produce a suspend, a lost request or a DAC
fault.

### What I have run on my DAC

My DAC runs this firmware, with its own name strings copied in.

On the DAC, from a PC, it enumerated at high speed. It played 44.1 kHz at
16 bit, 96 kHz at 24 bit, 192 kHz and 384 kHz at 32 bit, native DSD64 and
DoP64, each with the DAC reporting lock and unmuted. In the buffer
measurements it also played every rate from 44.1 kHz to 384 kHz at 32 bit,
48 kHz at 16 bit and 44.1 kHz at 24 bit. It passed the measurements under
"What I found", came back from three USB suspends at high speed and played,
and played through PipeWire. Its fault counters stayed at zero.

After those runs I made three more changes to the firmware: I restored a
DAC status check that the compiler had optimised away, I added the expiry of
a host pause, and I limited the buffer check to one reset a second. What I
have run since:

- With the first two changes: the DAC-dump test and the pause test from "The
  tool"; listening on the phone; a port reset in the middle of a stream,
  four times, after each of which the DAC came back at high speed and idle;
  28 runs of suspend, resume and playback starting 0 to 300 ms after the
  resume, half of them without a new sampling-frequency request, all of
  which played with correct feedback and a centred buffer; and 22 minutes of
  continuous 48 kHz playback, in which the buffer level read 192 or 216
  every 15 s and the firmware reset the buffer not once.
- With all three: eight plays following a 384 kHz to 48 kHz change, the four
  mid-stream recoveries, and a stream starved on purpose (see "An unsolved
  failure").

Then I added the counters. With them on the DAC: a 48 kHz stream through
the kernel driver showed one start, one reset at the start, none while
playing and a level between 216 and 240, and my own reads of the level
register during it gave 216 and 240. A stream starved on purpose showed a
level between 0 and 360 and one more reset each second, and the count ended
equal to register 0x96. Two suspends added two to the suspend count.

Then I added the wait through gaps in the data and, with it, a faster lock
setting for the DAC. With those on the DAC I ran the measurements under
"Gaps in the data", all with PCM. In the Android emulator described under
"Android monitor", Android played a tone through the DAC for 20 minutes
with two busy loops running in the guest and the monitor app logging every
second: the firmware counted 6,120 gaps, was not muted in any of the 1,200
readings taken while Android played, and reset the buffer 392 times; the
host reopened the stream once, and no request was lost.

That revision is the one I listened to next, on the phone, and it played
with a faint static. I took the DAC setting back out; "A DAC setting I
took back" has the account. The firmware in this repository is that
revision with the setting at the stock value again. It is on my DAC,
verified against the flash, and I have compared it register by register
with the revision that sounded clean, but I have not listened to it. I
have not played DoP or native DSD on it either.

### An unsolved failure

Once, about 15 to 20 minutes into listening on the phone, playback turned
into a repeating pattern of pops and short pauses. It stayed that way across
songs, and cleared after I had replugged the DAC and restarted the player
app a few times. It has not happened again and I have not reproduced it.

I can produce the same pattern in the firmware's counters by setting the
DAC to 384 kHz and sending it 48 kHz audio: the buffer sits empty, and the
buffer check, which then ran on every 21 ms pass, reset it about 25 times a
second. So the failure looks like the DAC not receiving audio as fast as it
was playing it. The suspend, port-reset and long-playback runs above were
my attempts to find a way into that state from the firmware's side, and
none did. I do not know whether the cause was in the phone or in a path I
have not tested.

What I changed: the buffer check now rests for a second after a reset. A
starved stream then gets one reset a second (measured) instead of 25.

A DAC that is unplugged forgets what it saw, so I also added the counters
above and the Android monitor below, to have a record from the phone the
next time it happens. That has not happened yet.

Running the monitor in an Android emulator with the DAC passed through
gave a second way to get pops and pauses, and this one I could fix. The
emulator's audio layer kept underrunning. Each time, the guest's USB audio
driver stopped sending and started again a few ms later without any
request, and the firmware, like the stock firmware, answered each stop by
muting and starting the stream over. In 74 s of Android playing a tone, the
firmware of that day started the stream 201 times and the DAC was muted in
44 of 62 readings, while the host changed neither rate nor format and the
buffer level read between 168 and 264 in 74 of 75 readings. With the
monitor app stopped, the stream was started 84 times in about 31 s, so the
app's reads were not the cause. "Gaps in the data" under "What I found"
has the measurements and what the firmware does now.

I do not know whether my phone ever stops sending like that, so I cannot
say this is what I heard.

### What it does not do

- Speed negotiation at attach still blocks the main loop.
- I have not tried a full-speed host.
- A DoP or native DSD stream is still stopped and started over at the first
  moment without data. I do not know what the bridge sends the DAC during a
  gap in those modes, and constant zeros are not silence in DSD.
- After a gap the DAC loses lock, and at 48 kHz about 290 ms pass before it
  is locked with no automute again. Staying unmuted through the gap does
  not shorten that (see "Gaps in the data").
- If a second request replaces the record of a format change before the
  interrupt handler has read it, the firmware counts the loss but cannot
  recover the format. I found no other register in banks 0 to 0x0f that
  follows the selected format.
- A sampling frequency the firmware has no table entry for (anything but
  32 kHz and 44.1 kHz to 768 kHz) is set up as 48 kHz. I have not tried one
  on hardware.
- Recovery of a missed request has only run in the emulator.
- The rate feedback is still wrong for 18 ms after a rate change (see "The
  playback buffer after a rate change").

## Android monitor

`android/` is a small app that reads the counters above from an Android
phone while the phone plays through the DAC. It needs no root. It sends the
same vendor control transfers as `ct7601-isp` and claims no interface, so
the phone's audio driver keeps the DAC. All it writes is the host's bank
selection and the firmware's command mailbox.

I have not run it on a phone. What I have run:

- Its code for reading the DAC, on a PC against my DAC during a 48 kHz
  stream: 18 readings a second apart, each 97 to 100 control transfers. The
  values agreed with `ct7601-isp status`, the buffer level stayed at 192 or
  216, and no reset was counted.
- The app in an Android 15 emulator, which has no DAC, reading a stand-in
  built into the app: it started its service, wrote the log, and Mark,
  Share log, Stop and a screen rotation worked. Without the stand-in it
  reported "No DAC attached" and then stopped looking, as described below.
- The app in the same emulator with my DAC passed through to it (QEMU's
  `usb-host` device on an emulated xHCI controller, after adding the
  `android.hardware.usb.host` feature to the system image). The guest
  kernel's USB audio driver bound the DAC. Android asked whether the app
  may access the DAC. Once allowed, the app read the DAC every second
  while Android played a tone through it at 48 kHz, 16 bit, with the audio
  driver still bound. When I reset the DAC from the PC, which the guest
  sees as an unplug and a replug, the app logged the failed read and "No
  DAC attached" and opened the DAC again by itself 3 s later; I had ticked
  "Always open" in Android's question. After a restart of the emulator
  Android asked again. In the 20-minute run under "What I have run on my
  DAC" the app took 1,207 readings a second apart and every one had the
  counters.

That is not how a phone plays to a USB DAC. This emulator's audio layer
plays to the guest's first sound card whatever it is. The DAC was the
first card in each of the seven boots with this firmware on it, and the
second in the one boot with the stock firmware, where Android then did not
play through it. After the DAC had been reset, Android did not play
through it again until the emulator was restarted. A phone also has a
different USB controller. So a phone remains untested.

    cd android && ./build.sh

builds `build/openskylark-monitor.apk`, signed with a debug key. It needs
the Android SDK's build-tools and the android-35 platform under
`$ANDROID_HOME` (default `~/Android/Sdk`) and a JDK; it does not use
Gradle. I built it with build-tools 35.0.0 and JDK 25. Install it with
`adb install`, or copy it to the phone and open it there.

Plug in the DAC, open the app and allow it to use the DAC. Once a second it
then reads the stream state, the counters, the frequency the host last
asked for, the buffer level, and bank 2 register 0x09, bank 6 0xf0 to 0xf7,
bank 7 0x05 and 0x43 to 0x49 and bank 8 0x06 to 0x0b, and appends them as a
row to a CSV file in `Download/openskylark` on the phone. The screen shows
the latest reading. I do not know what bank 7 0x43 to 0x49 hold; I log them
because 0x47 to 0x49 change while a stream plays and read zero when none
does.
**Mark** adds a row that says "mark", for noting the moment something is
heard. **Share log** hands the file to another app.

It looks for USB vendor id 2fc6. For another id, change `VENDOR_ID` in
`Bridge.java` and the id in `res/xml/device_filter.xml`.

The app takes no wake lock, and it opens the DAC only while Android reports
that music is playing and for 5 s after, unless the box on its screen is
ticked. I did that so that it changes as little as possible about when the
phone lets the DAC sleep. On my laptop, with no driver bound to the DAC and
autosuspend allowed, Linux suspended the DAC within a second, kept it awake
for as long as a program held its device file open without sending
anything, and suspended it again within two seconds of the file being
closed. I have not measured this on Android.

## Tests

    go test .                               # the tool's decoding, image editing and I2C pause
    android/build.sh test                   # the Android monitor's reading of the DAC, on a JVM
    cd fw && make test                      # the firmware, in the emulator
    cd fw && make test STOCK=../stock.bin   # and compared with the stock firmware

The emulator (`fw/emu`) is an 8051 core with a model of the chip's banked
registers and an I2C device. I inferred the model from the stock firmware.
It does not model USB traffic or timing. Where the real chip turned out to
behave differently, I changed the model (see "The serial port").

Without `STOCK`, 27 tests run on this firmware alone and 27 are skipped.
With it, all 54 run:

- The `port` build must make the same register writes in the same order as
  the stock firmware: through start-up (`TestBootPort`, which also compares
  the descriptor RAM at 0x1800 to 0x21ff), through a scripted session of
  rate and format changes with PCM, DoP and native DSD
  (`TestPlaybackSession`), and routine by routine over thousands of random
  cases.
- Most of the tests named under "What it does differently" also run the
  stock firmware and require it to show the defect.
- `TestPlaybackSession` holds this firmware to the stock firmware too,
  apart from the DAC input write, the buffer reset, the mute at bus reset,
  and status registers whose timing differs.
- `TestSettlesAfterAnySequence` sends 120 random sequences of rate, format,
  volume and mute requests, bus resets and data starting and stopping, then
  one ordinary 48 kHz stream, and requires the firmware to end up playing it
  unmuted and idle. This firmware does in all 120. The stock firmware does
  not in 11, where it loses the last format change.

The comparison tests know one build of the stock firmware: the image dated
"Sep 14 2021", 21,682 bytes long. They check a SHA-256 of its code with the
name-string table left out, and skip if the file given is anything else. My
DAC's firmware is that build. So is `CT7601_ES9018K2M.hex`, which came with
SXW's flashing tool and differs from mine only in the seven name strings.

`TestRobustStackAndPaging` checks that this firmware's stack stays below
0x70 (the stock firmware's start-up clears only internal RAM 0x00 to 0x7f)
and that it never executes `MOVX @Ri`.

`make listing STOCK=../stock.bin` disassembles that same stock build with
the names in `fw/notes.txt`, assembles the listing again and compares it
with the image.

## What I found

I measured everything in this section on my DAC from a PC running Linux 7.1,
unless it says "in the emulator". I measured the lost sample format on
unmodified stock firmware. For the buffer and transceiver measurements the
stock firmware carried two small binary patches of mine, one for the lost
sample format and one for the DAC input selection. Neither touches the
buffer handling or the attach code.

### The lost sample format

A USB audio host picks the sample format by selecting an "alternate
setting" of the audio interface, and sets the sampling frequency with a
separate request. The stock firmware loses the alternate setting when it
arrives within a few milliseconds of the frequency request. Its handler for
the frequency change reads the current alternate setting when it starts and
writes it back when it finishes; the interrupt handler stores the new one in
the same byte in between. The audio path then stays framed for the old
sample width while the host sends the new one.

I sent the two requests with a chosen gap and read back what the firmware
applied, counting the trials in which the new setting differed from the old.
With 0 to 2 ms between them the stock firmware kept the old setting in 11 of
11. With 5 ms or more it applied the new one in 10 of 10. In the emulator
the stock firmware and the `port` build keep the old setting at most of the
timings tried, and this firmware at none (`TestAlternateSettingRace`).

This firmware keeps the setting in a byte only the interrupt handler writes
and applies it again if it changed meanwhile. The same measurement then gave
the right setting in 6 of 6 trials at 0 to 2 ms.

The Linux kernel of an Android 15 emulator, with the DAC passed through to
it, does the same to the stock firmware. I had it open the DAC with a
different rate and sample width each time and read back the setting the
firmware had applied 1.5 s later. In the first 22 opens of the stock
firmware, eight were playing with the 16-bit setting applied after the host
had selected the 32-bit one: every 32-bit open at 44.1 kHz or 48 kHz. Six
more, all at 96 kHz, were still muted and I do not trust what I read from
them. This firmware had applied the setting the host selected in 60 of 60
opens.

### The playback buffer after a rate change

The firmware writes 503 to bank 7 registers 3 and 4, which I take to mean a
buffer of 504 channel samples. The level register moves by about 2 per
stereo frame at 16, 24 and 32 bit. It never read above 480 when I read its
two bytes consistently. After the path is reset it settles between 144 and
264, with the exception described further down.

The rate feedback the bridge gives the host did not change with the level:
whatever level a stream reached, it kept. And for about 18 ms after the host
changes the sampling frequency the feedback is wrong. Going from 384 kHz to
96 kHz it still read 48.0016 frames per microframe (the 384 kHz value) until
about 10 ms after the alternate setting was selected, then 21.2261 for 8 ms,
and only then 12.0004.

Linux selected the alternate setting 0.8 ms after the frequency request and
queued the first audio 0.3 ms after that. Its driver shifts the first
feedback value by powers of two until it lies within -25% and +50% of the
nominal rate, then follows any value from 12.5% below to 50% above nominal,
and redoes the shift after one outside that (`snd_usb_handle_sync_urb` in
`sound/usb/endpoint.c`, read in Linux 7.2). So it follows the wrong values
and over- or under-fills the buffer. I wrote a user-space player that
applies the same rule. Across 28 runs of a change from 384 kHz to 48 kHz it
ended start-up between 48 frames short and 184 frames over.

The buffer has less room than that. At 48 kHz, 110 extra frames left the
level at 408 to 456 depending on the sample width. 200 extra left it at 480
at all three widths, and 140 extra at two of the three (the 32-bit run read
432). 110, 140 or 200 too few left it at 0 to 24. Each stayed there for the
rest of its 1.5 s run. A buffer held at either end has no margin. I expect
it to keep dropping or repeating samples, but I did not measure that.

With plain `aplay` through the kernel driver, before the fix, 14 streams
each at a different rate from the one before settled anywhere from 24 to
456. The stream at 192 kHz and one of the two at 384 kHz sat at 24.

This is what a first play after plug-in looks like on Linux: the driver's
probe leaves the DAC set to 384 kHz, so the first stream at any other rate
starts with a rate change. Packets lost later have the same effect: 200
frames withheld mid-stream left the level at the bottom.

Resetting the path (bank 7 register 5, see "Protocol") brings the level back
to the middle, and the firmware's stream state does not react. At 352.8 kHz
and 384 kHz a reset now and then left the level at 408 instead (three times
in several dozen) and had to be repeated.

This firmware resets the path at the end of the 100 ms it already waits
before unmuting, checks the result and repeats the reset if needed. From the
main loop it checks a playing stream's level every 21 ms and resets the path
again when it reads out of range, at most once a second. With that, the same
14 streams settled between 144 and 264. Eight plays following a 384 kHz to
48 kHz change ended between 192 and 240 whatever the start-up error. A
stream knocked 130 or 200 frames either way was back in the middle with one
reset. Bank 0 registers 0x95 and 0x96 count the two kinds of reset and
`ct7601-isp status` prints them.

### Gaps in the data

A host whose audio underruns stops sending and starts again a few ms later
without any request; Linux's USB audio driver does this. The stock firmware
reads the bridge's data-present flag (bank 3 register 0x18 bit 3) on every
pass of its main loop and, the first time it is clear, mutes and starts the
stream over.

I measured this with a player on the PC that stops sending for a set time
three times a second, at 48 kHz, and sampled the firmware's muted flag
every 3 ms:

| Length of each stop | Stock firmware muted | This firmware muted |
|---|---|---|
| none | 0% | 0% |
| 0.5 ms | 27% | 0% |
| 1 ms to 20 ms | 32% to 35% | 0% |
| 40 ms | 40% | 0% |
| 80 ms | 46% | 0% |

For this firmware I sampled the mute pin as well, and it never came on. An
earlier revision of this firmware, which also started the stream over and
took longer about it, was muted 43% to 58% of the time for stops of 1 ms to
40 ms.

The mute is not all of it, and at 48 kHz it is not the larger part. The DAC
chip has two status bits (ES9018K2M register 0x40). By its data sheet, bit
0 is set while it is locked to its input, and bit 1 while its "automute
condition" holds: the audio data has stayed below a threshold for a set
time. The firmware sets both as the stock firmware does, the threshold to
-110 dB and the time to 68 ms at 48 kHz (registers 5 and 4; the time is
2096896 / (register 4 x 64 x the sampling rate) seconds), and has the DAC
ramp its volume down while the condition holds. With a quiet square wave
playing and a 3 ms stop every 500 ms, and that time at its shortest
(register 4 = 0xff, 2.7 ms at 48 kHz), I read the two bits about 15 times a
second:

| | Reads with the DAC locked and no automute |
|---|---|
| Stock firmware, 48 kHz | 38 of 90 |
| Stock firmware, 384 kHz | 63 of 93 |
| This firmware, 48 kHz | 47 of 111 |
| This firmware, 384 kHz | 80 of 104 |

So at 48 kHz each stop still costs about 290 ms, whatever the firmware
does with its mute: the DAC loses lock when the data returns. The data
sheet says how long it then takes. Register 10 sets the number of sample
periods that must pass before the DAC can lock, and at the stock value
that is 16,384: 341 ms at 48 kHz, 43 ms at 384 kHz, where the firmware's
own mute shows instead.

During a stop the bridge seems to send the DAC digital silence and keep its
clocks running. I conclude that from a run at 384 kHz with register 4 at
0xff: in 50 of 50 reads taken 40 ms to 90 ms into 90 ms stops bits 0 and 1
were both set, where with no stops bit 1 was clear in 593 of 593 reads.

The buffer mostly takes care of itself. After a stop the level was back
between 120 and 330 within about 3 ms, with no reset. A reset by the
firmware at the moment the data returned made things worse: in 20 s runs
with about 58 stops each, it gave up to 64 resets and 0.5% to 4.1% of the
time out of that range, against 2 to 10 resets and 0.1% to 2.3% without it.
So the firmware only has the level checked on its next 21 ms pass.

In the Android emulator, with the guest's processors kept busy so that its
audio layer underran, the firmware counted 171 gaps in 39 s. It started the
stream over for none of them, and was not muted in any of the app's 40
readings. The level check reset the buffer 12 times.

### A DAC setting I took back

The low four bits of the DAC's register 10 choose that number of sample
periods, from 16,384 at 0 down to 1,024 at 15. Changing only that register
on the stock firmware, from the PC, with the stops described above, took
the reads in which the DAC was unlocked at 48 kHz from 29 of 91 at the
stock value 0x00 to 6 of 91 at 0x04 and 2 of 94 at 0x0f. With 0x0f in this
firmware the DAC was locked with no automute in 90 of 112 reads at 48 kHz,
against the 47 of 111 in the table above, and with no stops about 100 reads
at each rate from 44.1 kHz to 384 kHz all showed it locked. On those
numbers I shipped 0x0f.

On my phone that revision played with a faint static that followed the
music, mostly in the left channel. The monitor app's log from the phone,
87 readings over 86 s with the static audible, showed nothing wrong on the
firmware's side: 48 kHz, the 32-bit setting the phone had selected, no
gaps, no buffer reset while playing, the buffer level between 192 and 216,
no lost request and no DAC fault.

That left the DAC setting, and I checked that it was all that was left. I
rebuilt the revision that had sounded clean; the result is byte for byte
the image I had flashed then. In the emulator I logged every register
write of that revision and of the one with the static through a 48 kHz,
32-bit stream. Outside the status bytes, the 935 writes up to the start of
playback are the same in the same order, neither writes anything else
while playing, and at the end every bridge register and every DAC register
holds the same value except DAC register 10. With register 10 back at 0x00
there is no difference at all, at 44.1, 48, 96, 192 or 384 kHz.

On the DAC itself I dumped all 4,096 bridge registers and 70 DAC registers
twice under each of the two revisions, the clean one and the one with
register 10 back at 0x00, while a 48 kHz tone played. Seventeen registers
changed between two dumps of the same revision. Of the rest, every DAC
register matched, and five bridge registers differed: the firmware's own
count of buffer resets; two that also differ from one stream to the next
of one revision (bank 7 0x0a and 0x43); one that goes up by one each time
the DAC is enumerated (bank 6 0xf8); and bank 0 register 0x44, which read
0x09 under one revision and 0x70 or 0x71 across five restarts of the other.
Whatever the firmware writes to that last one, it writes the same in both
revisions. I do not know what it is.

So the setting is back at the stock value, and `TestDACRegistersAsVendor`
now fails if a DAC register other than register 1 differs from what the
stock firmware leaves.

I had chosen 0x0f before I had the data sheet (ES9018K2M, revision 3.4). It
says of this field: "For compatibility with Rev. W, or when PCM data with
FSR > 96kHz is used, stop_div should be set to 4'd0 (16384 FSR edges)." So
0x0f was against the data sheet for every rate above 96 kHz, and the stock
firmware's 0 is the one value it allows at all the rates this DAC plays.
The data sheet does not say what a lower count does to the sound. The
chip's own default is 5 (2,730 periods, 57 ms at 48 kHz), and my DAC's chip
reports itself as revision V, so by the data sheet 5 is allowed up to
96 kHz. I have not tried it by ear, and I have nothing that measures the
DAC's output: neither of my PCs has a line input, and none of the numbers
above showed the static. A value can be tried by ear without flashing,
since

    sudo ./ct7601-isp i2c write 0x48 10 0x05

lasts until the DAC loses power or resumes from suspend.

### The DAC's clock

The DAC chip does not play the samples at the rate they arrive. The
ES9018K2M reports the ratio of the incoming sample rate to its own clock
(registers 0x42 to 0x45; the data sheet gives sample rate = number x clock
/ 2^32). I read 1894431 at 44.1 kHz, about 2061500 at 48 kHz, 4123632 at
96 kHz and 16492825 at 384 kHz. Each works out to a clock of 99.98 MHz to
100.00 MHz, which is 2,267 times the sample rate at 44.1 kHz and 260 times
at 384 kHz. So the DAC runs from a fixed 100 MHz clock of its own and
converts every stream to it, which is the mode its data sheet calls ASRC,
and it has to lock to each stream before it can.

The data sheet's other mode, with no conversion, needs a clock of exactly
128 times the sample rate. The CT7601 has a master clock output for a DAC
(pin 30 of the 48-pin package in Comtrue's brief data sheet), but the
ratios above show that the DAC's clock on this board does not come from it.

### The transceiver setting after a slow host

The stock firmware gives the host 200 ms from attach to agree high speed.
If it has not, the firmware detaches, steps a transceiver setting (bank
0x0f register 0x23) and attaches again, up to three times, and leaves the
setting wherever that ended. I held the host port off after a firmware
restart: 1 s of silence left the setting at step 1, and 3 s or more at
step 3, while the link still came up at high speed. The setting survived a
firmware restart.

Stepping it by hand during a 48 kHz stream changed nothing I could measure
from the host (no feedback packet errors in 2,500 at each step), so I do not
know whether it matters.

This firmware waits up to 3 s without detaching, starts from the default
setting and returns to it if the retries did not reach high speed. The same
test then left the default after 1 s, 3 s and 6 s.

### The serial port

The stock firmware prints its stream format on the 8051's serial port, with
the receiver and the serial interrupt enabled, and waits for the
transmit-done flag after each character. I once built this firmware with
both switched off. On the DAC its main loop hung at the first stream start,
at the point where it prints, so evidently the flag never came up. With the
stock set-up restored it ran. The firmware therefore uses the stock set-up
unchanged, and it stops waiting for a character after 1,024 machine cycles,
about a millisecond if a cycle is the microsecond the measured tick implies.

## Protocol

I recovered this by disassembling the vendor's Windows flashing tool,
`SXW_ISP_Tool.exe` (its main form's caption is "ComTrue USB Maintain
Tool"), and confirmed it by use. All requests use `bmRequestType` 0x43
(out) or 0xc3 (in).

| bRequest | Dir | wValue | wIndex | Data | Meaning |
|---|---|---|---|---|---|
| 0x90 | out | 0 | register | 1 byte | write register |
| 0x91 | in | 0 | register | 1 byte | read register |
| 0x84 | out | 0x5354 | 0x2080 | none | enter ISP mode, in which the flash is reached |
| 0x85 | out | 0x5835 | 0x8280 | none | exit ISP: chip resets and re-enumerates |
| 0x80 | out | 0 | 1 = end, 0 = more follows | SPI bytes | SPI write |
| 0x81 | out | bytes to read | 1 | SPI command | SPI write, then clock data in |
| 0x83 | in | 0 | 0 | 1 byte | non-zero when the SPI engine is idle |
| 0x82 | in | 0 | 0 | n bytes | data clocked in by 0x81 |

Register 0 selects a bank for the registers above it. What follows is what
the tool and the firmware use. I inferred the meanings from the stock
firmware and from watching my DAC.

- Bank 0x00. 0xa4 and 0xa6 are stream state published by the firmware. 0xa4
  is a rate code: a PCM rate index (0 = 44.1 kHz, 1 = 48 kHz, doubling every
  two up to 7 = 384 kHz), or for DoP the DSD rate (0 = DoP64), or for native
  DSD the DSD rate with bit 3 set (8 = DSD64). In 0xa6, bit 0 is "bus
  active" and bit 2 is "muted"; while a stream plays, bit 1 is clear in DoP
  mode and set otherwise.
  0x90 is a command mailbox to the firmware with parameters in 0x91 and 0x92
  and answers in 0xa0 to 0xa3; the firmware sets bit 0 of the command when it
  takes it. Command 0xd8 sets which parts of the main loop are skipped (bit 0
  events, bit 1 HID service and status publishing, bit 2 power handling,
  bit 7 the wake flags); 0xd0 returns four bytes of the firmware's RAM at
  the address in 0x91 (high byte) and 0x92.
  The firmware sets the bit before it writes the answer. With the tool
  reading the answer once, the first byte came back stale in two of three
  reads on my DAC. It now reads the answer until two readings agree, and 40
  reads in a row then returned the same count.
  The firmware's RAM at 0x2000 holds the last sampling frequency the host
  set, as four bytes, low byte first; it read 48000 during a 48 kHz stream.
  0xac is the low byte of the firmware's tick count; I measured a tick at
  0.5 ms.
- Bank 0x02, register 0x09: output pins. Bit 0 DSD, bit 1 mute, bit 2 bus
  active, bit 5 set once the DAC has been initialised.
- Bank 0x03, register 0x18: bit 3 is set while playback samples arrive,
  bit 1 while they carry DoP markers.
- Bank 0x06, 0xf0 to 0xf7: the SETUP packet of the last SET request or
  SET_INTERFACE the chip answered. The GET requests I tried (sampling
  frequency, clock validity, ranges, mute, volume, GET_INTERFACE, a string
  descriptor, device status) neither changed it nor raised a firmware event.
- Bank 0x07: the playback path. Register 0x05 is 0x50 while PCM plays and
  0x0a for DoP and native DSD; writing 0x02 and then the old value resets
  the path. 0x1d, with a ninth bit in 0x1e bit 0, is the buffer level.
- Bank 0x08: the playback clock. 0x06 and 0x07 read 480 during a 48 kHz
  stream; the firmware writes the samples per 10 ms there, and a 32-bit
  word for the clock generator to 0x08 to 0x0b.
- Bank 0x0d: SPI controller. After 0x84, write reg 7 = 0x10 and reg 1 = 3.
- Bank 0x0e: I2C master. Reg 0x11 = 4 to set up. Reg 0x10 control: 0xc0
  start, 0xa0 stop, 0x80 receive with ACK, 0x90 receive with NACK; reads back
  bit 0 = done, bit 3 = no ACK. Reg 0x14 = 1 then reg 0x12 = byte sends;
  reg 0x14 = 3 receives into reg 0x13.
- Bank 0x0f, register 0x23, bits 6:5: the USB transceiver setting described
  under "The transceiver setting after a slow host". I do not know what it
  adjusts.

The firmware sees the same registers in XDATA at 0x4000 plus the register
number.

The chip answers standard and class control requests by itself, from tables
the firmware builds in RAM, and tells the firmware afterwards. With the
firmware's event handling switched off, requests were still answered
correctly, and a sampling frequency the firmware does not support
(22050 Hz) was accepted and read back.

Flash access is plain JEDEC SPI through 0x80/0x81: 0x9f id, 0x05 status,
0x03 read (64 bytes per request), 0x06 write enable, 0x01 write status,
0x20 sector erase, 0x02 page program (256 bytes, or 32 when the id starts
c2 22; that case comes from the vendor tool and I have not exercised it).

## Firmware image

An image is 8051 code with no length field and no checksum. I have booted
stock images with bytes changed and with code added past the original end,
and this firmware, which is shorter than the stock one.

The USB identity sits in a table marked `CTUSB_Table01`. Offsets from the
marker: VID +0x0d and PID +0x0f (big-endian), then 32-byte space-padded
strings at +0x11 manufacturer, +0x31 product, +0x51 serial, +0x71 play port,
+0x91 record port, +0xb1 secondary play port, the UAC1 product id at +0xd1,
and the UAC1 product name at +0xd3. This firmware keeps the same table, so
`ct7601-isp image info` and `image set` work on stock images and on its own.

## Licence

Zero-Clause BSD, see `LICENSE`.
