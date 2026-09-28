package kvmd

import (
	"bytes"
	"context"
	"image/jpeg"
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
