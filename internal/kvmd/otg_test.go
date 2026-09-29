package kvmd

import (
	"context"
	"testing"
	"time"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func TestOTGFunctionsDefault(t *testing.T) {
	c := New(kvmdfake.New(t).Device(), time.Second)
	otg, err := c.OTGFunctions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if otg.StartCDROM || otg.StartFlash || !otg.Ready || otg.Applying || otg.ApplyError != "" {
		t.Fatalf("%+v", otg)
	}
}

func TestSetOTGStartCDROM(t *testing.T) {
	ctx := context.Background()
	c := New(kvmdfake.New(t).Device(), time.Second)
	if err := c.SetOTGStartCDROM(ctx, true); err != nil {
		t.Fatal(err)
	}
	otg, err := c.OTGFunctions(ctx)
	if err != nil || !otg.StartCDROM {
		t.Fatalf("%+v %v", otg, err)
	}
	if err := c.SetOTGStartCDROM(ctx, false); err != nil {
		t.Fatal(err)
	}
	otg, err = c.OTGFunctions(ctx)
	if err != nil || otg.StartCDROM {
		t.Fatalf("%+v %v", otg, err)
	}
}

func TestOTGFunctionsApplyError(t *testing.T) {
	ctx := context.Background()
	f := kvmdfake.New(t)
	f.FailOTGApply("gadget rebuild failed")
	c := New(f.Device(), time.Second)
	if err := c.SetOTGStartCDROM(ctx, true); err != nil {
		t.Fatal(err)
	}
	otg, err := c.OTGFunctions(ctx)
	if err != nil || otg.ApplyError != "gadget rebuild failed" {
		t.Fatalf("%+v %v", otg, err)
	}
	// The queued failure is consumed once: a second apply reports no error.
	if err := c.SetOTGStartCDROM(ctx, false); err != nil {
		t.Fatal(err)
	}
	otg, err = c.OTGFunctions(ctx)
	if err != nil || otg.ApplyError != "" {
		t.Fatalf("%+v %v", otg, err)
	}
}

// TestMSDOnlineFollowsStartCDROM pins the controller ruling that msd.online
// is not independent state: the device only serves the selected image to
// the host while start_cdrom is on.
func TestMSDOnlineFollowsStartCDROM(t *testing.T) {
	ctx := context.Background()
	c := New(kvmdfake.New(t).Device(), time.Second)

	st, err := c.MSD(ctx)
	if err != nil || st.Online {
		t.Fatalf("want online=false by default: %+v %v", st, err)
	}

	if err := c.SetOTGStartCDROM(ctx, true); err != nil {
		t.Fatal(err)
	}
	st, err = c.MSD(ctx)
	if err != nil || !st.Online {
		t.Fatalf("want online=true after start_cdrom on: %+v %v", st, err)
	}

	if err := c.SetOTGStartCDROM(ctx, false); err != nil {
		t.Fatal(err)
	}
	st, err = c.MSD(ctx)
	if err != nil || st.Online {
		t.Fatalf("want online=false after start_cdrom off: %+v %v", st, err)
	}
}
