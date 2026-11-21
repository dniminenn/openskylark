package openskylark.monitor;

import java.io.IOException;

/**
 * The CT7601's vendor requests, used the way ct7601-isp uses them
 * (bridge.go). Only registers and firmware RAM are read; the one thing
 * written is the host's own bank selection and the firmware's command
 * mailbox.
 */
final class Bridge {
    /** One control transfer on endpoint 0. */
    interface Control {
        /** Returns the number of bytes moved, or throws. */
        int transfer(int requestType, int request, int value, int index, byte[] data, int length) throws IOException;
    }

    static final int VENDOR_ID = 0x2fc6;

    static final int OUT = 0x43, IN = 0xc3; // vendor request, recipient "other"
    static final int REG_WRITE = 0x90, REG_READ = 0x91;

    static final int REG_BANK = 0x00;
    static final int MAIL_COMMAND = 0x90, MAIL_PARAM_1 = 0x91, MAIL_PARAM_2 = 0x92, MAIL_ANSWER = 0xa0;
    static final int MAIL_READ_RAM = 0xd0;

    static final int TELEMETRY_ADDR = 0x27f0, TELEMETRY_MAGIC = 0x54;
    static final int REQUESTED_RATE_ADDR = 0x2000;

    private final Control usb;
    private final byte[] one = new byte[1];

    Bridge(Control usb) {
        this.usb = usb;
    }

    int read(int reg) throws IOException {
        if (usb.transfer(IN, REG_READ, 0, reg, one, 1) != 1) {
            throw new IOException("short read of register " + Integer.toHexString(reg));
        }
        return one[0] & 0xff;
    }

    void write(int reg, int value) throws IOException {
        one[0] = (byte) value;
        if (usb.transfer(OUT, REG_WRITE, 0, reg, one, 1) != 1) {
            throw new IOException("short write of register " + Integer.toHexString(reg));
        }
    }

    private int[] read(int reg, int count) throws IOException {
        int[] v = new int[count];
        for (int i = 0; i < count; i++) {
            v[i] = read(reg + i);
        }
        return v;
    }

    /**
     * Four bytes of the firmware's RAM, read with host command 0xd0. Bank 0
     * must be selected. Returns null if no firmware takes the command.
     */
    int[] ram(int addr) throws IOException {
        write(MAIL_PARAM_1, addr >> 8);
        write(MAIL_PARAM_2, addr & 0xff);
        write(MAIL_COMMAND, MAIL_READ_RAM);
        long deadline = System.nanoTime() + 300_000_000L;
        while (read(MAIL_COMMAND) != (MAIL_READ_RAM | 1)) {
            if (System.nanoTime() > deadline) {
                return null;
            }
        }
        // The firmware marks a command taken before it writes the answer,
        // so the answer is read until two readings agree.
        int[] prev = null;
        for (int tries = 0; tries < 9; tries++) {
            int[] b = read(MAIL_ANSWER, 4);
            if (prev != null && java.util.Arrays.equals(b, prev)) {
                return b;
            }
            prev = b;
        }
        throw new IOException("the answer to a firmware RAM read never settled");
    }

    /** Everything the monitor logs, read in one pass. */
    Sample sample() throws IOException {
        Sample s = new Sample();
        int bank = read(REG_BANK);
        try {
            write(REG_BANK, 0);
            s.rate = read(0xa4);
            s.aux = read(0xa5);
            s.flags = read(0xa6);
            s.countStart = read(0x95);
            s.countPlaying = read(0x96);
            s.countGaps = read(0x98);
            int[] first = ram(TELEMETRY_ADDR);
            if (first != null && first[0] == TELEMETRY_MAGIC && first[1] == 1) {
                int[] t = new int[16];
                System.arraycopy(first, 0, t, 0, 4);
                for (int off = 4; off < 16 && t != null; off += 4) {
                    int[] part = ram(TELEMETRY_ADDR + off);
                    if (part == null) {
                        t = null;
                    } else {
                        System.arraycopy(part, 0, t, off, 4);
                    }
                }
                s.telemetry = t;
            }
            if (first != null) {
                int[] hz = ram(REQUESTED_RATE_ADDR);
                if (hz != null) {
                    s.requestedHz = (long) hz[0] | (long) hz[1] << 8 | (long) hz[2] << 16 | (long) hz[3] << 24;
                }
            }
            write(REG_BANK, 7);
            s.pathControl = read(0x05);
            int low = read(0x1d);
            int high = read(0x1e);
            s.level = (high & 1) << 8 | low;
            s.feedback = read(0x43, 7);
            write(REG_BANK, 8);
            s.clock = read(0x06, 6);
            write(REG_BANK, 6);
            s.lastSetup = read(0xf0, 8);
            write(REG_BANK, 2);
            s.pins = read(0x09);
        } finally {
            write(REG_BANK, bank);
        }
        return s;
    }
}
