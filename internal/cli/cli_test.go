package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func runCLI(t *testing.T, f *kvmdfake.Server, args ...string) (string, string, int) {
	t.Helper()
	d := f.Device()
	cfg := map[string]any{
		"devices":        map[string]any{"t": map[string]any{"url": d.URL, "user": d.User, "password": d.Password}},
		"default_device": "t",
	}
	b, _ := json.Marshal(cfg)
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb strings.Builder
	code := Execute(append([]string{"--config", p}, args...), &out, &errb)
	return out.String(), errb.String(), code
}

func TestPortShowAndSwitch(t *testing.T) {
	f := kvmdfake.New(t)
	out, _, code := runCLI(t, f, "port", "-o", "json")
	if code != 0 || !strings.Contains(out, `"active": 4`) {
		t.Fatalf("code %d out %s", code, out)
	}
	_, _, code = runCLI(t, f, "port", "2")
	if code != 0 {
		t.Fatalf("switch code %d", code)
	}
	calls := f.Calls()
	found := false
	for _, c := range calls {
		if c.Path == "/api/switch/set_active" {
			found = true
			if c.Query.Get("port") != "1.2" {
				t.Fatalf("sent port=%s", c.Query.Get("port"))
			}
		}
	}
	if !found {
		t.Fatal("no set_active call")
	}
}

func TestPortRejectsBadInput(t *testing.T) {
	f := kvmdfake.New(t)
	for _, arg := range []string{"0", "5", "1.3", "abc"} {
		_, stderr, code := runCLI(t, f, "port", arg)
		if code != ExitUsage || !strings.Contains(stderr, "port must be 1 to 4") {
			t.Fatalf("%s: code %d stderr %s", arg, code, stderr)
		}
	}
	for _, c := range f.Calls() {
		if c.Path == "/api/switch/set_active" {
			t.Fatal("bad input reached the device")
		}
	}
}

func TestScreenshotWritesFile(t *testing.T) {
	f := kvmdfake.New(t)
	p := filepath.Join(t.TempDir(), "s.png")
	out, _, code := runCLI(t, f, "screenshot", "--file", p, "--png", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d out %s", code, out)
	}
	b, _ := os.ReadFile(p)
	if len(b) < 8 || string(b[1:4]) != "PNG" || !strings.Contains(out, `"width": 1200`) {
		t.Fatalf("bad png or output %s", out)
	}
}

func TestScreenshotNoSignal(t *testing.T) {
	f := kvmdfake.New(t)
	f.SetSource(false, "invalid_resolution:1200x750@60", 0, 0)
	p := filepath.Join(t.TempDir(), "s.jpg")
	_, stderr, code := runCLI(t, f, "screenshot", "--file", p)
	if code != ExitDevice || !strings.Contains(stderr, "invalid_resolution:1200x750@60") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("file written despite no signal")
	}
}

func TestStatus(t *testing.T) {
	f := kvmdfake.New(t)
	out, _, code := runCLI(t, f, "status", "-o", "json")
	if code != 0 || !strings.Contains(out, `"profile": "chromebook"`) || !strings.Contains(out, `"model": "RM4PE"`) {
		t.Fatalf("code %d out %s", code, out)
	}
}

func TestMissingConfigExit3(t *testing.T) {
	var out, errb strings.Builder
	code := Execute([]string{"--config", filepath.Join(t.TempDir(), "none.json"), "status"}, &out, &errb)
	if code != ExitConfig {
		t.Fatalf("code %d", code)
	}
}

func TestWideConfigModeWarns(t *testing.T) {
	f := kvmdfake.New(t)
	d := f.Device()
	b, _ := json.Marshal(map[string]any{"devices": map[string]any{"t": map[string]any{"url": d.URL, "user": d.User, "password": d.Password}}, "default_device": "t"})
	p := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(p, b, 0o644)
	var out, errb strings.Builder
	code := Execute([]string{"--config", p, "port"}, &out, &errb)
	if code != 0 || !strings.Contains(errb.String(), "chmod 600") {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
}

func TestPortNextAndPrev(t *testing.T) {
	f := kvmdfake.New(t)
	f.ActivePort = 1 // port 2, away from both edges so next/prev actually move
	out, _, code := runCLI(t, f, "port", "next", "-o", "json")
	if code != 0 || !strings.Contains(out, `"active": 3`) || !strings.Contains(out, `"changed": true`) {
		t.Fatalf("next: code %d out %s", code, out)
	}
	found := false
	for _, c := range f.Calls() {
		if c.Path == "/api/switch/set_active_next" {
			found = true
		}
	}
	if !found {
		t.Fatal("no set_active_next call")
	}

	out, _, code = runCLI(t, f, "port", "prev", "-o", "json")
	if code != 0 || !strings.Contains(out, `"active": 2`) || !strings.Contains(out, `"changed": true`) {
		t.Fatalf("prev: code %d out %s", code, out)
	}
	found = false
	for _, c := range f.Calls() {
		if c.Path == "/api/switch/set_active_prev" {
			found = true
		}
	}
	if !found {
		t.Fatal("no set_active_prev call")
	}
}

// TestPortNextNoWrapAtLastPort pins the live fact that set_active_next does
// not wrap: from the last port (the fake's default, 1.4) it is a no-op,
// reported as such instead of waiting out the settle timeout.
func TestPortNextNoWrapAtLastPort(t *testing.T) {
	f := kvmdfake.New(t) // default ActivePort is 3 (port 4, the last port)
	out, _, code := runCLI(t, f, "port", "next", "-o", "json")
	if code != 0 || !strings.Contains(out, `"active": 4`) || !strings.Contains(out, `"changed": false`) {
		t.Fatalf("code %d out %s", code, out)
	}
	out, _, code = runCLI(t, f, "port", "next")
	if code != 0 || !strings.Contains(out, "already at the last port") {
		t.Fatalf("text: code %d out %s", code, out)
	}
}

// TestPortPrevNoWrapAtFirstPort is TestPortNextNoWrapAtLastPort's mirror:
// set_active_prev does not wrap either, so from the first port it is a
// no-op.
func TestPortPrevNoWrapAtFirstPort(t *testing.T) {
	f := kvmdfake.New(t)
	f.ActivePort = 0 // port 1, the first port
	out, _, code := runCLI(t, f, "port", "prev", "-o", "json")
	if code != 0 || !strings.Contains(out, `"active": 1`) || !strings.Contains(out, `"changed": false`) {
		t.Fatalf("code %d out %s", code, out)
	}
	out, _, code = runCLI(t, f, "port", "prev")
	if code != 0 || !strings.Contains(out, "already at the first port") {
		t.Fatalf("text: code %d out %s", code, out)
	}
}

func TestPortSettleNoSignalIsNotError(t *testing.T) {
	f := kvmdfake.New(t)
	f.VideoLinks[0] = false
	f.SetSource(false, "no signal", 0, 0)
	out, stderr, code := runCLI(t, f, "port", "1", "--settle", "50ms")
	if code != 0 {
		t.Fatalf("code %d stderr %s out %s", code, stderr, out)
	}
	if !strings.Contains(out, "no signal") {
		t.Fatalf("out %s", out)
	}
}

func TestPortSettleTimesOutWhenSignalExpected(t *testing.T) {
	f := kvmdfake.New(t)
	f.SetSource(false, "invalid_resolution:1200x750@60", 0, 0)
	_, stderr, code := runCLI(t, f, "port", "2", "--settle", "50ms")
	if code != ExitDevice {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestScreenshotStdoutJSONIsUsageError(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "screenshot", "--file", "-", "-o", "json")
	if code != ExitUsage {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestScreenshotWaitSignalRetriesUntilValid(t *testing.T) {
	f := kvmdfake.New(t)
	f.SetSource(false, "invalid_resolution:1200x750@60", 0, 0)
	go func() {
		time.Sleep(80 * time.Millisecond)
		f.SetSource(true, "1200x752@60", 1200, 752)
	}()
	p := filepath.Join(t.TempDir(), "s.jpg")
	out, stderr, code := runCLI(t, f, "screenshot", "--file", p, "--wait-signal", "500ms")
	if code != 0 {
		t.Fatalf("code %d stderr %s out %s", code, stderr, out)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

func TestScreenshotStdoutWritesBytes(t *testing.T) {
	f := kvmdfake.New(t)
	out, stderr, code := runCLI(t, f, "screenshot")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if len(out) < 4 || out[0] != 0xff || out[1] != 0xd8 {
		t.Fatalf("out does not look like a jpeg: %q", out[:min(len(out), 16)])
	}
}
