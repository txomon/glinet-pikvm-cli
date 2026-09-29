package kvmd

import (
	"bytes"
	"context"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func TestStreamerAndSnapshot(t *testing.T) {
	f := kvmdfake.New(t)
	c := New(f.Device(), 5*time.Second)
	s, err := c.Streamer(context.Background())
	if err != nil || s.Valid() != nil || s.Width != 1200 {
		t.Fatalf("%+v %v", s, err)
	}
	b, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != 1200 {
		t.Fatalf("decode %v", err)
	}
}

func TestStreamerInvalid(t *testing.T) {
	f := kvmdfake.New(t)
	f.SetSource(false, "invalid_resolution:1200x750@60", 0, 0)
	s, err := New(f.Device(), time.Second).Streamer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Valid() == nil {
		t.Fatal("invalid capture reported valid")
	}
}

func TestSnapshotNoSignalIsError(t *testing.T) {
	f := kvmdfake.New(t)
	f.SetSource(false, "no signal", 0, 0)
	if _, err := New(f.Device(), time.Second).Snapshot(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// TestStreamerValidStages covers each stage a live device was observed to
// pass through after an EDID flash, on the way from an old capture (old:
// 800x600@60) to a new one (new: 1920x1080@60). Only the fully settled
// stage is valid; every earlier stage fails a different condition.
func TestStreamerValidStages(t *testing.T) {
	cases := []struct {
		name  string
		state StreamerState
		valid bool
	}{
		{
			name:  "offline",
			state: StreamerState{Online: false, HDMISignal: true, RealResolution: "800x600@60", Width: 800, Height: 600},
			valid: false,
		},
		{
			name: "stage1 hdmi signal down, still reporting the old mode",
			state: StreamerState{
				Online: true, HDMISignal: false,
				RealResolution: "800x600@60", Width: 800, Height: 600,
			},
			valid: false,
		},
		{
			name: "stage2 real_resolution briefly no_signal (device spelling, underscore)",
			state: StreamerState{
				Online: true, HDMISignal: false,
				RealResolution: "no_signal", Width: 800, Height: 600,
			},
			valid: false,
		},
		{
			name: "stage3 real_resolution updated but hdmi signal and reported resolution still lag",
			state: StreamerState{
				Online: true, HDMISignal: false,
				RealResolution: "1920x1080@60", Width: 800, Height: 600,
			},
			valid: false,
		},
		{
			name: "stage4 hdmi signal back but the reported resolution still lags",
			state: StreamerState{
				Online: true, HDMISignal: true,
				RealResolution: "1920x1080@60", Width: 800, Height: 600,
			},
			valid: false,
		},
		{
			name: "stage5 fully settled",
			state: StreamerState{
				Online: true, HDMISignal: true,
				RealResolution: "1920x1080@60", Width: 1920, Height: 1080,
			},
			valid: true,
		},
		{
			name:  "legacy no signal spelling (space, not the device's own)",
			state: StreamerState{Online: false, HDMISignal: false, RealResolution: "no signal", Width: 0, Height: 0},
			valid: false,
		},
		{
			name:  "invalid_resolution prefix",
			state: StreamerState{Online: true, HDMISignal: true, RealResolution: "invalid_resolution:1200x750@60", Width: 0, Height: 0},
			valid: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.state.Valid()
			if tc.valid && err != nil {
				t.Fatalf("expected valid, got error: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("expected an error, got nil")
			}
			if err != nil && !strings.Contains(err.Error(), tc.state.RealResolution) {
				t.Fatalf("error %q does not quote real_resolution %q", err, tc.state.RealResolution)
			}
		})
	}
}

func TestParsedResolution(t *testing.T) {
	cases := []struct {
		real   string
		wantW  int
		wantH  int
		wantOK bool
	}{
		{"1920x1080@60", 1920, 1080, true},
		{"1200x752@59.94", 1200, 752, true},
		{"no_signal", 0, 0, false},
		{"no signal", 0, 0, false},
		{"invalid_resolution:1200x750@60", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tc := range cases {
		s := StreamerState{RealResolution: tc.real}
		w, h, ok := s.ParsedResolution()
		if ok != tc.wantOK || (ok && (w != tc.wantW || h != tc.wantH)) {
			t.Fatalf("%q: got w=%d h=%d ok=%v, want w=%d h=%d ok=%v", tc.real, w, h, ok, tc.wantW, tc.wantH, tc.wantOK)
		}
	}
}
