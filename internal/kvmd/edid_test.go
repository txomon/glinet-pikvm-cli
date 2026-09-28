package kvmd

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/txomon/glinet-pikvm-cli/internal/edid"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func TestEDIDRoundTrip(t *testing.T) {
	f := kvmdfake.New(t)
	c := New(f.Device(), time.Second)
	list, err := c.EDIDList(context.Background())
	if err != nil || len(list) == 0 {
		t.Fatalf("%v %v", list, err)
	}
	var fourK string
	for _, p := range list {
		if p.Key == "E3840x2160" {
			fourK = p.Content
		}
	}
	if fourK == "" {
		t.Fatal("no 4k preset in fake")
	}
	if err := c.FlashEDID(context.Background(), fourK); err != nil {
		t.Fatal(err)
	}
	got, _ := c.GetEDID(context.Background())
	if got != fourK {
		t.Fatal("flashed edid not read back")
	}
	calls := f.Calls()
	last := calls[len(calls)-2]
	if last.Path != "/api/upgrade/edid" || !bytes.HasPrefix(last.Body, []byte("edid=00ffffffffffff00")) {
		t.Fatalf("flash call %+v", last)
	}
	_ = edid.ChromebookHex
}
