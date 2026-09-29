package cli

import (
	"path/filepath"
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

func TestDoctorMissingConfigStillListsAllChecks(t *testing.T) {
	var out, errb strings.Builder
	p := filepath.Join(t.TempDir(), "none.json")
	code := Execute([]string{"--config", p, "doctor", "-o", "json"}, &out, &errb)
	if code != ExitDevice {
		t.Fatalf("code %d out %s err %s", code, out.String(), errb.String())
	}
	want := []string{"config", "reach", "auth", "version", "switch", "capture", "hid", "msd", "otg", "mouse"}
	last := -1
	for _, name := range want {
		idx := strings.Index(out.String(), `"name": "`+name+`"`)
		if idx < 0 {
			t.Fatalf("missing check %q in %s", name, out.String())
		}
		if idx < last {
			t.Fatalf("check %q out of order in %s", name, out.String())
		}
		last = idx
	}
}

func TestDevicesHidesPassword(t *testing.T) {
	f := kvmdfake.New(t)
	out, _, code := runCLI(t, f, "devices")
	if code != 0 || strings.Contains(out, f.Password) {
		t.Fatalf("code %d out %s", code, out)
	}
}
