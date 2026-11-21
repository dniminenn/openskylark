package openskylark.monitor;

/** Run by build.sh with a plain JVM: java -cp build/classes-test openskylark.monitor.BridgeTest */
public final class BridgeTest {
    private static int failures;

    private static void check(boolean ok, String what) {
        if (!ok) {
            failures++;
            System.out.println("FAIL: " + what);
        }
    }

    public static void main(String[] args) throws Exception {
        String want = "01,00,13,2,3,2,3,4,5,192,216,7,8,9,10,11,12,15,13,48000,50,216,"
                + "00000000000000,000000000000,0000000000000000,24";

        // Answers that arrive late must not be read early.
        for (int late = 0; late <= 3; late++) {
            FakeChip chip = new FakeChip();
            chip.late = late;
            chip.bank = 5;
            Sample s = new Bridge(chip).sample();
            check(s.csv().equals(want), "late " + late + ": row " + s.csv());
            check(chip.bank == 5, "late " + late + ": bank left at " + chip.bank);
        }

        FakeChip chip = new FakeChip();
        Sample s = new Bridge(chip).sample();
        check(s.format().equals("48 kHz"), "format " + s.format());
        check(s.summary().contains("192 to 216 in the last second"), "summary " + s.summary());
        check(s.csv().split(",", -1).length == Sample.CSV_HEADER.split(",").length, "columns");
        System.out.println(chip.transfers + " transfers a sample");

        // Nothing played in the last second: no level range.
        chip = new FakeChip();
        chip.ram[Bridge.TELEMETRY_ADDR + 6] = 0xff;
        chip.ram[Bridge.TELEMETRY_ADDR + 7] = 0;
        s = new Bridge(chip).sample();
        check(s.csv().startsWith("01,00,13,2,3,2,3,4,5,,,7,8,"), "idle: row " + s.csv());
        check(!s.summary().contains("last second"), "idle: summary " + s.summary());

        // Firmware without the block: the registers still read.
        chip = new FakeChip();
        chip.ram[Bridge.TELEMETRY_ADDR] = 0;
        s = new Bridge(chip).sample();
        check(s.telemetry == null && s.requestedHz == 48000 && s.level == 216, "no block: " + s.csv());
        check(s.csv().split(",", -1).length == Sample.CSV_HEADER.split(",").length, "columns without the block");

        // No firmware answering: the registers still read, after one wait.
        chip = new FakeChip();
        chip.deaf = true;
        long t0 = System.nanoTime();
        s = new Bridge(chip).sample();
        long ms = (System.nanoTime() - t0) / 1_000_000;
        check(s.telemetry == null && s.requestedHz == -1 && s.level == 216 && s.countPlaying == 3, "deaf: " + s.csv());
        check(ms >= 250 && ms < 450, "deaf: took " + ms + " ms");

        // Formats.
        s = new Sample();
        s.rate = 0x08;
        s.flags = 0x03;
        check(s.format().equals("DSD64"), "DSD64 reads " + s.format());
        s.rate = 0x00;
        s.flags = 0x01;
        check(s.format().equals("DoP64"), "DoP64 reads " + s.format());
        s.rate = 0x07;
        s.flags = 0x17;
        check(s.format().equals("384 kHz"), "384 kHz reads " + s.format());

        if (failures > 0) {
            System.exit(1);
        }
        System.out.println("ok");
    }
}
