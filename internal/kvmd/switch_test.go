package kvmd

import (
	"context"
	"testing"
	"time"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func TestSwitchState(t *testing.T) {
	f := kvmdfake.New(t)
	c := New(f.Device(), 5*time.Second)
	s, err := c.Switch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveID != "1.4" || s.ActivePort != 3 || len(s.Ports) != 4 || s.Ports[0].ID != "1.1" {
		t.Fatalf("got %+v", s)
	}
}

func TestSetActivePortSendsDottedID(t *testing.T) {
	f := kvmdfake.New(t)
	c := New(f.Device(), 5*time.Second)
	if err := c.SetActivePort(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	last := calls[len(calls)-1]
	if last.Path != "/api/switch/set_active" || last.Query.Get("port") != "1.2" {
		t.Fatalf("got %+v", last)
	}
	s, _ := c.Switch(context.Background())
	if s.ActiveID != "1.2" {
		t.Fatalf("active %s", s.ActiveID)
	}
}

func TestSetActivePortRange(t *testing.T) {
	c := New(kvmdfake.New(t).Device(), time.Second)
	for _, n := range []int{0, 5, -1} {
		if err := c.SetActivePort(context.Background(), n); err == nil {
			t.Fatalf("port %d accepted", n)
		}
	}
}
