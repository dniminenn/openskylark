package main

import "testing"

// Register values read from the test unit while each format was playing.
func TestStreamFormat(t *testing.T) {
	for _, c := range []struct {
		rate, flags byte
		want        string
	}{
		{0x01, 0x13, "48 kHz"},
		{0x05, 0x13, "192 kHz"},
		{0x08, 0x13, "DSD64"},
		{0x00, 0x11, "DoP64"},
		{0x00, 0x13, "44.1 kHz"},
		{0x01, 0x15, "48 kHz"}, // idle and muted
	} {
		if got := (streamStatus{rate: c.rate, flags: c.flags}).format(); got != c.want {
			t.Errorf("a4=%02x a6=%02x: %q, want %q", c.rate, c.flags, got, c.want)
		}
	}
}
