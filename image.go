package main

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// The firmware carries its USB identity in a table that starts with this
// marker. Offsets below are relative to the marker.
const tableMagic = "CTUSB_Table01"

const (
	tableVID     = 0x0d // big-endian
	tablePID     = 0x0f
	tableUAC1PID = 0xd1
	tableStrLn   = 32 // space padded
)

type tableString struct {
	name   string
	offset int
}

// The vendor tool edits the first five. The last two are named after what a
// 2023 SXW build keeps in them.
var tableStrings = []tableString{
	{"manufacturer", 0x11},
	{"product", 0x31},
	{"serial", 0x51},
	{"play", 0x71},
	{"record", 0x91},
	{"play2", 0xb1},
	{"uac1-product", 0xd3},
}

var buildDate = regexp.MustCompile(`(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) [ 0-9][0-9] 20[0-9]{2}`)

// trimImage drops the erased tail of a full flash dump.
func trimImage(img []byte) []byte {
	return bytes.TrimRight(img, "\xff")
}

func findTable(img []byte) (int, error) {
	i := bytes.Index(img, []byte(tableMagic))
	if i < 0 || i+0xd3+tableStrLn > len(img) {
		return 0, errors.New("no " + tableMagic + " in image: not CT7601 firmware")
	}
	return i, nil
}

func imageInfo(img []byte) (string, error) {
	t, err := findTable(img)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "code size     %d bytes\n", len(trimImage(img)))
	fmt.Fprintf(&b, "build date    %s\n", buildDate.Find(img))
	fmt.Fprintf(&b, "table offset  %#x\n", t)
	fmt.Fprintf(&b, "usb id        %02x%02x:%02x%02x\n", img[t+tableVID], img[t+tableVID+1], img[t+tablePID], img[t+tablePID+1])
	fmt.Fprintf(&b, "uac1 pid      %02x%02x\n", img[t+tableUAC1PID], img[t+tableUAC1PID+1])
	for _, s := range tableStrings {
		v := strings.TrimRight(string(img[t+s.offset:t+s.offset+tableStrLn]), " ")
		fmt.Fprintf(&b, "%-13s %q\n", s.name, v)
	}
	return b.String(), nil
}

// imageLike returns a copy of img carrying the whole identity table of
// other: ids and every text field.
func imageLike(img, other []byte) ([]byte, error) {
	to, err := findTable(img)
	if err != nil {
		return nil, err
	}
	from, err := findTable(other)
	if err != nil {
		return nil, fmt.Errorf("the image to copy from: %w", err)
	}
	out := bytes.Clone(img)
	copy(out[to:to+0xd3+tableStrLn], other[from:from+0xd3+tableStrLn])
	return out, nil
}

// imageSet returns a copy of img with table fields replaced. Keys are
// "vid", "pid" or a tableStrings name.
func imageSet(img []byte, fields map[string]string) ([]byte, error) {
	t, err := findTable(img)
	if err != nil {
		return nil, err
	}
	out := bytes.Clone(img)
	for key, val := range fields {
		switch key {
		case "vid", "pid", "uac1-pid":
			var id uint16
			if _, err := fmt.Sscanf(val, "%x", &id); err != nil || len(val) != 4 {
				return nil, fmt.Errorf("%s %q: want 4 hex digits", key, val)
			}
			off := t + map[string]int{"vid": tableVID, "pid": tablePID, "uac1-pid": tableUAC1PID}[key]
			out[off], out[off+1] = byte(id>>8), byte(id)
		default:
			off := -1
			for _, s := range tableStrings {
				if s.name == key {
					off = t + s.offset
				}
			}
			if off < 0 {
				return nil, fmt.Errorf("unknown field %q", key)
			}
			if len(val) >= tableStrLn {
				return nil, fmt.Errorf("%s: at most %d characters", key, tableStrLn-1)
			}
			copy(out[off:off+tableStrLn], fmt.Sprintf("%-*s", tableStrLn, val))
		}
	}
	return out, nil
}
