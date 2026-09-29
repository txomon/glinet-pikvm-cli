package cli

import (
	"strings"
	"testing"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func TestDoctorAllGreen(t *testing.T) {
	out, e, code := runCLI(t, kvmdfake.New(t), "doctor", "-o", "json")
	if code != 0 || strings.Contains(out, `"ok": false`) {
		t.Fatalf("code %d out %s err %s", code, out, e)
	}
}

func TestDoctorBadPassword(t *testing.T) {
	f := kvmdfake.New(t)
	f.Password = "other"
	out, _, code := runCLI(t, f, "doctor", "-o", "json")
	if code != ExitDevice || !strings.Contains(out, `"name": "auth"`) {
		t.Fatalf("code %d out %s", code, out)
	}
}

func TestDevicesHidesPassword(t *testing.T) {
	f := kvmdfake.New(t)
	out, _, code := runCLI(t, f, "devices")
	if code != 0 || strings.Contains(out, f.Password) {
		t.Fatalf("code %d out %s", code, out)
	}
}
