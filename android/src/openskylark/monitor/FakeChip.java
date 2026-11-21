package openskylark.monitor;

/**
 * Stands in for the DAC: the registers the monitor reads and a firmware
 * that answers host command 0xd0. Used by the tests, and by the app when it
 * is started with the "fake" extra so that it can be run without a DAC.
 */
final class FakeChip implements Bridge.Control {
    final int[][] regs = new int[16][256];
    final int[] ram = new int[0x10000];
    int bank;
    int late;          // register reads between taking a command and each answer byte
    boolean deaf;      // no firmware takes commands
    int transfers;

    private int polls, wait, owedAt, owedLeft;

    FakeChip() {
        int[] block = {Bridge.TELEMETRY_MAGIC, 1, 2, 3, 4, 5, 96, 108, 7, 8, 9, 10, 11, 12, 0x15, 0x13};
        for (int i = 0; i < block.length; i++) {
            ram[Bridge.TELEMETRY_ADDR + i] = block[i];
        }
        ram[Bridge.REQUESTED_RATE_ADDR] = 0x80;
        ram[Bridge.REQUESTED_RATE_ADDR + 1] = 0xbb;
        regs[0][0xa4] = 0x01;
        regs[0][0xa6] = 0x13;
        regs[0][0x95] = 2;
        regs[0][0x96] = 3;
        regs[0][0x98] = 6;
        regs[7][0x05] = 0x50;
        regs[7][0x1d] = 0xd8;
        regs[2][0x09] = 0x24;
    }

    @Override
    public int transfer(int requestType, int request, int value, int index, byte[] data, int length) {
        transfers++;
        int reg = index & 0xff;
        if (request == Bridge.REG_WRITE) {
            if (reg == Bridge.REG_BANK) {
                bank = data[0] & 0x0f;
            } else {
                regs[bank][reg] = data[0] & 0xff;
                if (bank == 0 && reg == Bridge.MAIL_COMMAND) {
                    polls = 0;
                }
            }
        } else if (reg == Bridge.REG_BANK) {
            data[0] = (byte) bank;
        } else {
            if (owedLeft > 0 && --wait <= 0) {
                answer();
                wait = late;
            }
            int cmd = regs[0][Bridge.MAIL_COMMAND];
            if (bank == 0 && reg == Bridge.MAIL_COMMAND && !deaf && cmd != 0 && (cmd & 1) == 0 && ++polls >= 3) {
                if (cmd == Bridge.MAIL_READ_RAM) {
                    owedAt = regs[0][Bridge.MAIL_PARAM_1] << 8 | regs[0][Bridge.MAIL_PARAM_2];
                    owedLeft = 4;
                    wait = late;
                    while (late == 0 && owedLeft > 0) {
                        answer();
                    }
                }
                regs[0][Bridge.MAIL_COMMAND] = cmd | 1;
            }
            data[0] = (byte) regs[bank][reg];
        }
        return length;
    }

    private void answer() {
        int i = 4 - owedLeft;
        regs[0][Bridge.MAIL_ANSWER + i] = ram[owedAt + i];
        owedLeft--;
    }
}
