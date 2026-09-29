package cli

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/txomon/glinet-pikvm-cli/internal/edid"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

// fixChecksum recomputes byte 127 of the base block so the sum of bytes
// 0..127 is 0 mod 256, after a test has hand-patched some other byte.
func fixChecksum(t *testing.T, hexStr string) string {
	t.Helper()
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	if len(raw) < 128 {
		t.Fatalf("edid too short: %d bytes", len(raw))
	}
	sum := 0
	for _, b := range raw[:127] {
		sum += int(b)
	}
	raw[127] = byte((256 - sum%256) % 256)
	return hex.EncodeToString(raw)
}

func TestEdidSetProfileFlashesAndReadsBack(t *testing.T) {
	f := kvmdfake.New(t)
	// The fake never moves its streamer resolution to match a flashed
	// profile (see TestEdidSetSettlesThroughStages for that), so without a
	// short --settle this would wait out the full 15s default every time.
	out, stderr, code := runCLI(t, f, "edid", "set", "4k", "--settle", "50ms", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d out %s err %s", code, out, stderr)
	}
	if !strings.HasPrefix(f.CurrentEDID(), "00ffffffffffff00328d") {
		t.Fatal("4k not flashed")
	}
	if !strings.Contains(out, `"profile": "4k"`) {
		t.Fatalf("out %s", out)
	}
}

func TestEdidSetUnchanged(t *testing.T) {
	f := kvmdfake.New(t) // fake starts with the chromebook edid
	out, _, code := runCLI(t, f, "edid", "set", "chromebook", "-o", "json")
	if code != 0 || !strings.Contains(out, `"changed": false`) {
		t.Fatalf("code %d out %s", code, out)
	}
	for _, c := range f.Calls() {
		if c.Path == "/api/upgrade/edid" {
			t.Fatal("flashed an identical edid")
		}
	}
}

func TestEdidSetRejectsUncapturable(t *testing.T) {
	f := kvmdfake.New(t)
	// 1200x750: patch the chromebook DTD vActive low byte f0 -> ee and fix checksum
	b := []byte(edid.ChromebookHex)
	copy(b[59*2:], "ee")
	hexStr := string(b)
	p := filepath.Join(t.TempDir(), "e.hex")
	_ = os.WriteFile(p, []byte(fixChecksum(t, hexStr)), 0o600)
	_, stderr, code := runCLI(t, f, "edid", "set", "--file", p)
	if code != ExitUsage || !strings.Contains(stderr, "divisible by 4") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestEdidValidateOffline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "e.hex")
	_ = os.WriteFile(p, []byte(edid.ChromebookHex), 0o600)
	var out, errb strings.Builder
	code := Execute([]string{"--config", "/nonexistent", "edid", "validate", p}, &out, &errb)
	if code != 0 || !strings.Contains(out.String(), "1200x752") {
		t.Fatalf("code %d out %s err %s", code, out.String(), errb.String())
	}
}

func TestEdidUnknownProfile(t *testing.T) {
	_, _, code := runCLI(t, kvmdfake.New(t), "edid", "set", "8k")
	if code != ExitUsage {
		t.Fatalf("code %d", code)
	}
}

// TestEdidSetThenStatusReportsProfile flashes a preset profile and confirms
// status's matchEDIDProfile matches it back by name, covering its preset
// branch (a Task 7 reviewer flagged that branch as untested).
func TestEdidSetThenStatusReportsProfile(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "edid", "set", "4k", "--settle", "50ms")
	if code != 0 {
		t.Fatalf("set code %d stderr %s", code, stderr)
	}
	out, _, code := runCLI(t, f, "status", "-o", "json")
	if code != 0 || !strings.Contains(out, `"profile": "4k"`) {
		t.Fatalf("code %d out %s", code, out)
	}
}

// TestEdidSetSettlesThroughStages plays the exact five-stage transition a
// live device was observed to go through after a flash (hdmi signal drops,
// real_resolution briefly reports "no_signal", real_resolution updates
// ahead of the reported resolution, hdmi signal returns, and only then does
// the reported resolution catch up) and confirms "edid set" reports the new
// resolution only once every field has settled, never on an earlier stage.
func TestEdidSetSettlesThroughStages(t *testing.T) {
	f := kvmdfake.New(t)
	f.ScriptStreamer(
		kvmdfake.StreamerStage{Online: true, HDMISignal: false, Real: "800x600@60", Width: 800, Height: 600},
		kvmdfake.StreamerStage{Online: true, HDMISignal: false, Real: "no_signal", Width: 800, Height: 600},
		kvmdfake.StreamerStage{Online: true, HDMISignal: false, Real: "1920x1080@60", Width: 800, Height: 600},
		kvmdfake.StreamerStage{Online: true, HDMISignal: true, Real: "1920x1080@60", Width: 800, Height: 600},
		kvmdfake.StreamerStage{Online: true, HDMISignal: true, Real: "1920x1080@60", Width: 1920, Height: 1080},
	)
	out, stderr, code := runCLI(t, f, "edid", "set", "1k", "--settle", "3s", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d out %s err %s", code, out, stderr)
	}
	if !strings.Contains(out, `"capture": "1920x1080@60"`) {
		t.Fatalf("did not report the settled resolution: out %s", out)
	}
	if strings.Contains(out, "800x600") || strings.Contains(out, "no_signal") {
		t.Fatalf("reported an intermediate stage instead of waiting: out %s", out)
	}
}
