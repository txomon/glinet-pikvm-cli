package kvmd

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/txomon/glinet-pikvm-cli/internal/config"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func TestAuthHeadersAndForbidden(t *testing.T) {
	f := kvmdfake.New(t)
	d := f.Device()
	d.Password = "wrong"
	_, err := New(d, time.Second).Switch(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 403 {
		t.Fatalf("want 403 APIError, got %v", err)
	}
}

func TestOkFalseIsError(t *testing.T) {
	f := kvmdfake.New(t)
	f.FailNext["/api/switch/set_active_next"] = kvmdfake.Failure{Status: 200, Kind: "SwitchError", Msg: "no switch"}
	err := New(f.Device(), time.Second).SetActiveNext(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Kind != "SwitchError" || ae.Message != "no switch" {
		t.Fatalf("got %v", err)
	}
}

func TestVersion(t *testing.T) {
	v, err := New(kvmdfake.New(t).Device(), time.Second).Version(context.Background())
	if err != nil || v.Model != "RM4PE" {
		t.Fatalf("%+v %v", v, err)
	}
}

// TestReachTransportFailure checks Reach reports an error when the request
// never gets an HTTP response at all, as opposed to an authless 401/403
// (covered by the doctor "reach" tests in internal/cli, against the fake).
// A listener opened then immediately closed leaves its port refusing
// connections, without depending on any address actually being unused.
func TestReachTransportFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	d := config.Device{URL: "http://" + addr, User: "admin", Password: "pw"}
	if err := New(d, time.Second).Reach(context.Background()); err == nil {
		t.Fatal("want an error for a closed port, got nil")
	}
}
