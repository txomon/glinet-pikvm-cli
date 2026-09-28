package edid

import (
	"strings"
	"testing"
)

const fourKBase = "00ffffffffffff00328d323100888888201e0103800c07780a0dc9a05747982712484c0000000101010101010101010101010101010104740030f2705a80b0588a000070f800001e023a801871382d40582c4500c48e2100001e000000fd00186414961e000a202020202020000000fc004c6f6e7469756d2073656d690a0164"

func TestParseChromebook(t *testing.T) {
	m, err := Parse(ChromebookHex)
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 1200 || m.Height != 752 || m.FPS() != 60 || m.PixelClockKHz != 73250 {
		t.Fatalf("got %+v", m)
	}
	if err := CheckCapture(m); err != nil {
		t.Fatal(err)
	}
}

func TestParse4K(t *testing.T) {
	m, err := Parse(fourKBase)
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 3840 || m.Height != 2160 || m.FPS() != 30 {
		t.Fatalf("got %+v", m)
	}
}

func TestNormalize(t *testing.T) {
	spaced := strings.ToUpper(ChromebookHex[:100]) + " \n\t" + ChromebookHex[100:]
	n, err := Normalize(spaced)
	if err != nil || n != ChromebookHex {
		t.Fatalf("err %v", err)
	}
	if _, err := Normalize(fourKBase); err != nil {
		t.Fatalf("128-byte edid rejected: %v", err)
	}
}

func TestNormalizeRejects(t *testing.T) {
	bad := []string{
		"",
		"zz" + ChromebookHex[2:],
		ChromebookHex[:300],
		"11" + ChromebookHex[2:], // header
		ChromebookHex[:254] + "00" + ChromebookHex[256:], // base checksum
	}
	for i, s := range bad {
		if _, err := Normalize(s); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestCheckCaptureRules(t *testing.T) {
	cases := []struct {
		m  Mode
		ok bool
	}{
		{Mode{Width: 1200, Height: 752, RefreshHz: 59.81}, true},
		{Mode{Width: 1200, Height: 750, RefreshHz: 60}, false},
		{Mode{Width: 1920, Height: 1080, RefreshHz: 144}, false},
		{Mode{Width: 100, Height: 752, RefreshHz: 60}, false},
		{Mode{Width: 3840, Height: 2160, RefreshHz: 30}, true},
	}
	for _, c := range cases {
		if err := CheckCapture(c.m); (err == nil) != c.ok {
			t.Fatalf("%v: err %v", c.m, err)
		}
	}
}

func TestPresetKey(t *testing.T) {
	if k, _, _ := PresetKey("4k"); k != "E3840x2160" {
		t.Fatal(k)
	}
	if k, hex, _ := PresetKey("chromebook"); k != "" || hex != ChromebookHex {
		t.Fatal("chromebook")
	}
	if _, _, err := PresetKey("8k"); err == nil {
		t.Fatal("8k accepted")
	}
}
