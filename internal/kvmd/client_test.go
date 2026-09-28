package kvmd

import (
	"context"
	"errors"
	"testing"
	"time"

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
