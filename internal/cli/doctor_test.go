package cli

import (
	"encoding/json"
	"net"
	"os"
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
	code := Execute([]string{"--config", p, "doctor", "-o", "json"}, strings.NewReader(""), &out, &errb)
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

// TestDoctorReachFailureSkipsRemainingDeviceChecks pins fix round 3's
// finding 9: once reach fails, every later device check used to still run
// anyway, multiplying a blackholed address's timeout by up to 9x for a
// report that was always going to fail. A closed local listener refuses
// the connection immediately, so reach fails fast (no timeout) while still
// exercising the same code path. All 10 checks must still appear, in
// order, with the skipped ones named as such.
func TestDoctorReachFailureSkipsRemainingDeviceChecks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cfg := map[string]any{
		"devices":        map[string]any{"t": map[string]any{"url": "http://" + addr, "user": "admin", "password": "pw"}},
		"default_device": "t",
	}
	b, _ := json.Marshal(cfg)
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb strings.Builder
	code := Execute([]string{"--config", p, "doctor", "-o", "json"}, strings.NewReader(""), &out, &errb)
	if code != ExitDevice {
		t.Fatalf("code %d out %s err %s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), `"name": "reach"`) || !strings.Contains(out.String(), `"ok": false`) {
		t.Fatalf("out %s", out.String())
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
	if !strings.Contains(out.String(), "skipped: reach failed") {
		t.Fatalf("remaining checks not marked skipped: %s", out.String())
	}
}

func TestDevicesHidesPassword(t *testing.T) {
	f := kvmdfake.New(t)
	out, _, code := runCLI(t, f, "devices")
	if code != 0 || strings.Contains(out, f.Password) {
		t.Fatalf("code %d out %s", code, out)
	}
}
