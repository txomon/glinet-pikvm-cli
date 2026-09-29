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

// TestDoctorFailsWhenReferencedFileUnreadable pins the config check's extra
// duty: it enumerates the selected device's file references and fails the
// check (without ever printing the file's contents) when one cannot be
// read. f.Device's own resolution failure for the same missing file is
// suppressed, so the problem is reported exactly once, not twice.
func TestDoctorFailsWhenReferencedFileUnreadable(t *testing.T) {
	f := kvmdfake.New(t)
	d := f.Device()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cfg := map[string]any{
		"devices": map[string]any{"t": map[string]any{
			"url":           d.URL,
			"user":          d.User,
			"password_file": missing,
			"insecure_tls":  d.InsecureTLS,
		}},
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
	if !strings.Contains(out.String(), `"name": "config"`) || !strings.Contains(out.String(), `"ok": false`) {
		t.Fatalf("config check did not fail: %s", out.String())
	}

	var envelope struct {
		Result []doctorCheck `json:"result"`
	}
	if err := json.Unmarshal([]byte(out.String()), &envelope); err != nil {
		t.Fatalf("bad json: %v: %s", err, out.String())
	}
	var configDetail string
	found := false
	for _, c := range envelope.Result {
		if c.Name == "config" {
			configDetail = c.Detail
			found = true
		}
	}
	if !found {
		t.Fatalf("no config check in %s", out.String())
	}
	if !strings.Contains(configDetail, "password_file") || !strings.Contains(configDetail, "unreadable") {
		t.Fatalf("missing per-file readability detail: %q", configDetail)
	}
	if n := strings.Count(configDetail, "unreadable"); n != 1 {
		t.Fatalf("want exactly one \"unreadable\" mention, got %d: %q", n, configDetail)
	}
	// f.Device's own resolution error, if not suppressed, would restate the
	// same problem as "device \"t\": password: read ...".
	if strings.Contains(configDetail, `device "t": password:`) {
		t.Fatalf("f.Device's redundant resolution error was not suppressed: %q", configDetail)
	}
}

// doctorConfigDetail writes cfg as the config file, runs doctor against it,
// and returns the "config" check's detail text.
func doctorConfigDetail(t *testing.T, cfg map[string]any) string {
	t.Helper()
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
	var envelope struct {
		Result []doctorCheck `json:"result"`
	}
	if err := json.Unmarshal([]byte(out.String()), &envelope); err != nil {
		t.Fatalf("bad json: %v: %s", err, out.String())
	}
	for _, c := range envelope.Result {
		if c.Name == "config" {
			return c.Detail
		}
	}
	t.Fatalf("no config check in %s", out.String())
	return ""
}

// TestDoctorReportsUnrelatedResolutionErrorAlongsideUnreadableFile is a
// regression test: a single fileCheckFailed bool used to suppress ALL of
// f.Device's error text once ANY referenced file failed its readability
// check, even when that error was for a completely different, unrelated
// problem (no url configured at all). The suppression must be scoped to
// only the specific field/path a file check already reported.
func TestDoctorReportsUnrelatedResolutionErrorAlongsideUnreadableFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	configDetail := doctorConfigDetail(t, map[string]any{
		"devices": map[string]any{"t": map[string]any{
			"user":          "admin",
			"password_file": missing,
		}},
		"default_device": "t",
	})
	if !strings.Contains(configDetail, "password_file") || !strings.Contains(configDetail, "unreadable") {
		t.Fatalf("missing per-file readability detail: %q", configDetail)
	}
	if !strings.Contains(configDetail, "has no url") {
		t.Fatalf("unrelated resolution error (no url) was dropped: %q", configDetail)
	}
}
