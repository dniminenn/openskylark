package openskylark.monitor;

/** One reading of the DAC. */
final class Sample {
    int rate, aux, flags;            // bank 0 0xa4-0xa6, kept by the firmware
    int countStart, countPlaying;    // bank 0 0x95, 0x96
    int countGaps;                   // bank 0 0x98
    int[] telemetry;                 // 16 bytes (fw/src/ct7601.h), or null on firmware without them
    long requestedHz = -1;           // the host's last sampling-frequency request
    int pathControl, level;          // bank 7 0x05; 0x1d with bit 0 of 0x1e
    int[] feedback;                  // bank 7 0x43-0x49
    int[] clock;                     // bank 8 0x06-0x0b
    int[] lastSetup;                 // bank 6 0xf0-0xf7, the last SET request's SETUP packet
    int pins;                        // bank 2 0x09

    static final String CSV_HEADER =
            "a4,a5,a6,count_start,count_playing,count_gaps,"
            + "t_resets_start,t_resets_playing,t_data_stopped,t_starts,t_level_min,t_level_max,"
            + "t_lost_requests,t_dac_faults,t_bus_resets,t_suspends,t_rate_requests,t_format_changes,"
            + "t_state,t_rate_alt,requested_hz,path_control,level,feedback_43_49,clock_06_0b,last_setup,pins";

    private static final String[] RATES = {
        "44.1 kHz", "48 kHz", "88.2 kHz", "96 kHz", "176.4 kHz", "192 kHz", "352.8 kHz", "384 kHz", "705.6 kHz", "768 kHz"
    };

    private static String hex(int v) {
        return String.format("%02x", v);
    }

    private static String hex(int[] v) {
        StringBuilder b = new StringBuilder();
        for (int x : v) {
            b.append(hex(x));
        }
        return b.toString();
    }

    String csv() {
        StringBuilder b = new StringBuilder();
        b.append(hex(rate)).append(',').append(hex(aux)).append(',').append(hex(flags)).append(',');
        b.append(countStart).append(',').append(countPlaying).append(',').append(countGaps).append(',');
        if (telemetry == null) {
            b.append(",,,,,,,,,,,,,,");
        } else {
            int[] t = telemetry;
            b.append(t[2]).append(',').append(t[3]).append(',').append(t[4]).append(',').append(t[5]).append(',');
            if (t[6] <= t[7]) {
                b.append(t[6] * 2).append(',').append(t[7] * 2).append(',');
            } else {
                b.append(",,"); // nothing played in the last second
            }
            for (int i = 8; i <= 13; i++) {
                b.append(t[i]).append(',');
            }
            b.append(hex(t[14])).append(',').append(hex(t[15])).append(',');
        }
        if (requestedHz >= 0) {
            b.append(requestedHz);
        }
        b.append(',').append(hex(pathControl)).append(',').append(level).append(',');
        b.append(hex(feedback)).append(',').append(hex(clock)).append(',').append(hex(lastSetup)).append(',').append(hex(pins));
        return b.toString();
    }

    /** The format the firmware reports, decoded as ct7601-isp does. */
    String format() {
        int code = rate & 0x0f;
        boolean playing = (flags & 4) == 0;
        if ((code & 8) != 0 && code < 12) {
            return "DSD" + (64 << (code & 3));
        }
        if (playing && (flags & 2) == 0 && code < 4) {
            return "DoP" + (64 << code);
        }
        if (code < RATES.length) {
            return RATES[code];
        }
        return "rate ?";
    }

    String summary() {
        StringBuilder b = new StringBuilder();
        b.append((flags & 4) != 0 ? "Muted" : "Playing").append(", ").append(format());
        if (requestedHz >= 0) {
            b.append("\nHost asked for ").append(requestedHz).append(" Hz");
        }
        b.append("\nGaps in the data bridged: ").append(countGaps);
        b.append("\nBuffer level ").append(level);
        if (telemetry == null) {
            b.append("\nBuffer resets: ").append(countStart).append(" at a start, ").append(countPlaying).append(" while playing");
            b.append("\n(this firmware keeps no other counters)");
            return b.toString();
        }
        int[] t = telemetry;
        if (t[6] <= t[7]) {
            b.append(", ").append(t[6] * 2).append(" to ").append(t[7] * 2).append(" in the last second");
        }
        b.append("\nBuffer resets: ").append(t[2]).append(" at a start, ").append(t[3]).append(" while playing");
        b.append("\nStream starts ").append(t[5]).append(", data stopped ").append(t[4]);
        b.append("\nLost requests ").append(t[8]).append(", DAC faults ").append(t[9]);
        b.append("\nBus resets ").append(t[10]).append(", suspends ").append(t[11]);
        b.append("\nRate requests ").append(t[12]).append(", format changes ").append(t[13]);
        return b.toString();
    }
}
