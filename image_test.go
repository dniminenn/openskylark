package main

import (
	"strings"
	"testing"
)

// testImage is some code around an identity table with the given ids and
// the same text in every field.
func testImage(vid, pid [2]byte, text string, pad int) []byte {
	img := []byte(strings.Repeat("\x02", pad) + tableMagic)
	img = append(img, vid[0], vid[1], pid[0], pid[1])
	for i := 0; i < 6; i++ {
		img = append(img, []byte(text+strings.Repeat(" ", tableStrLn-len(text)))...)
	}
	img = append(img, 0xf8, 0x41)
	img = append(img, []byte(text+strings.Repeat(" ", tableStrLn-len(text)))...)
	return append(img, 0x22, 0x22)
}

func TestImageLike(t *testing.T) {
	built := testImage([2]byte{0x2f, 0xc6}, [2]byte{0xf8, 0x01}, "default name", 40)
	unit := testImage([2]byte{0x12, 0x34}, [2]byte{0x56, 0x78}, "name on the unit", 7)
	out, err := imageLike(built, unit)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := imageInfo(out)
	want, _ := imageInfo(unit)
	strip := func(s string) string { // everything after the table offset line
		return s[strings.Index(s, "usb id"):]
	}
	if strip(got) != strip(want) {
		t.Errorf("identity not carried over:\n%s\nwant\n%s", got, want)
	}
	if len(out) != len(built) || string(out[:40]) != string(built[:40]) || string(out[len(out)-2:]) != "\x22\x22" {
		t.Error("bytes outside the table changed")
	}
	if _, err := imageLike(built, []byte("not firmware")); err == nil {
		t.Error("no error for a source without a table")
	}
	changed, err := imageSet(out, map[string]string{"product": "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := imageInfo(changed); !strings.Contains(info, `product       "renamed"`) || !strings.Contains(info, "1234:5678") {
		t.Errorf("a field set after -like:\n%s", info)
	}
	changed, err = imageSet(out, map[string]string{"uac1-pid": "abcd"})
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := imageInfo(changed); !strings.Contains(info, "uac1 pid      abcd") || !strings.Contains(info, "1234:5678") {
		t.Errorf("after setting the UAC1 product id:\n%s", info)
	}
}
