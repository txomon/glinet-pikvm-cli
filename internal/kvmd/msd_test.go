package kvmd

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func TestMSDLifecycle(t *testing.T) {
	f := kvmdfake.New(t)
	c := New(f.Device(), 5*time.Second)
	ctx := context.Background()
	data := bytes.Repeat([]byte{0xAB}, 4096)
	if err := c.MSDUpload(ctx, "test.img", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	st, err := c.MSD(ctx)
	if err != nil || st.Images["test.img"].Size != 4096 || !st.Images["test.img"].Complete {
		t.Fatalf("%+v %v", st, err)
	}
	if err := c.MSDSetParams(ctx, "test.img", false, false); err != nil {
		t.Fatal(err)
	}
	if err := c.MSDSetConnected(ctx, true); err != nil {
		t.Fatal(err)
	}
	st, _ = c.MSD(ctx)
	if !st.Drive.Connected || st.Drive.Image != "test.img" || st.Drive.CDROM {
		t.Fatalf("drive %+v", st.Drive)
	}
	if err := c.MSDRemove(ctx, "test.img"); err == nil {
		t.Fatal("removing a connected image should fail")
	}
	_ = c.MSDSetConnected(ctx, false)
	if err := c.MSDRemove(ctx, "test.img"); err != nil {
		t.Fatal(err)
	}
	st, _ = c.MSD(ctx)
	if _, ok := st.Images["test.img"]; ok {
		t.Fatal("image still present")
	}
}

func TestMSDNullImage(t *testing.T) {
	st, err := New(kvmdfake.New(t).Device(), time.Second).MSD(context.Background())
	if err != nil || st.Drive.Image != "" || st.Drive.Connected {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestMSDSetParamsUnknownImage(t *testing.T) {
	c := New(kvmdfake.New(t).Device(), time.Second)
	err := c.MSDSetParams(context.Background(), "nope.img", false, false)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != "MsdUnknownImageError" {
		t.Fatalf("want MsdUnknownImageError, got %v", err)
	}
}

func TestMSDRemoveUnknownImage(t *testing.T) {
	c := New(kvmdfake.New(t).Device(), time.Second)
	err := c.MSDRemove(context.Background(), "nope.img")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != "MsdUnknownImageError" {
		t.Fatalf("want MsdUnknownImageError, got %v", err)
	}
}
