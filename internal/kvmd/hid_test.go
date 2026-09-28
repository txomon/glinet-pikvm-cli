package kvmd

import (
	"context"
	"testing"
	"time"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func TestHIDCalls(t *testing.T) {
	f := kvmdfake.New(t)
	c := New(f.Device(), time.Second)
	ctx := context.Background()
	if err := c.SendShortcut(ctx, []string{"ControlLeft", "AltLeft", "Delete"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Print(ctx, "hello world", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.MouseMove(ctx, -32768, 32767); err != nil {
		t.Fatal(err)
	}
	if err := c.MouseButton(ctx, "left"); err != nil {
		t.Fatal(err)
	}
	if err := c.MouseWheel(ctx, 0, -3); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	want := []struct{ path, key, val string }{
		{"/api/hid/events/send_shortcut", "keys", "ControlLeft,AltLeft,Delete"},
		{"/api/hid/print", "limit", "0"},
		{"/api/hid/events/send_mouse_move", "to_x", "-32768"},
		{"/api/hid/events/send_mouse_button", "button", "left"},
		{"/api/hid/events/send_mouse_wheel", "delta_y", "-3"},
	}
	for i, w := range want {
		c := calls[i]
		if c.Path != w.path || c.Query.Get(w.key) != w.val {
			t.Fatalf("call %d: %+v", i, c)
		}
	}
	if string(calls[1].Body) != "hello world" || calls[1].Query.Get("keymap") != "en-us" {
		t.Fatalf("print call %+v", calls[1])
	}
}

func TestMouseDeltaRange(t *testing.T) {
	c := New(kvmdfake.New(t).Device(), time.Second)
	if err := c.MouseWheel(context.Background(), 0, 200); err == nil {
		t.Fatal("delta 200 accepted")
	}
}

func TestUnknownKeyRejectedByDevice(t *testing.T) {
	c := New(kvmdfake.New(t).Device(), time.Second)
	if err := c.SendKey(context.Background(), "NoSuchKey"); err == nil {
		t.Fatal("expected ValidatorError from fake")
	}
}
