package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func TestRunBatch(t *testing.T) {
	f := kvmdfake.New(t)
	js := `[{"type":"key","keys":"f13"},{"type":"wait","ms":1},{"type":"port","port":2},{"type":"type","text":"hi"}]`
	out, e, code := runCLI(t, f, "run", "--actions-json", js, "-o", "json")
	if code != 0 || !strings.Contains(out, `"completed": 4`) {
		t.Fatalf("code %d out %s err %s", code, out, e)
	}
}

func TestRunValidatesBeforeSending(t *testing.T) {
	f := kvmdfake.New(t)
	js := `[{"type":"key","keys":"f13"},{"type":"port","port":9}]`
	_, stderr, code := runCLI(t, f, "run", "--actions-json", js)
	if code != ExitUsage || !strings.Contains(stderr, "action 1") {
		t.Fatalf("code %d %s", code, stderr)
	}
	if len(paths(f, "/api/hid/")) != 0 {
		t.Fatal("sent actions before validating the batch")
	}
}

func TestRunStopsOnFailure(t *testing.T) {
	f := kvmdfake.New(t)
	f.FailNext["/api/hid/print"] = kvmdfake.Failure{Status: 500, Kind: "HidError", Msg: "boom"}
	js := `[{"type":"key","keys":"f13"},{"type":"type","text":"x"},{"type":"key","keys":"f14"}]`
	out, _, code := runCLI(t, f, "run", "--actions-json", js, "-o", "json")
	if code != ExitDevice || !strings.Contains(out, `"completed": 1`) {
		t.Fatalf("code %d out %s", code, out)
	}
	for _, p := range paths(f, "/api/hid/events/send_key") {
		if strings.Contains(p, "F14") {
			t.Fatal("continued after failure")
		}
	}
}

func TestRunMouseActions(t *testing.T) {
	f := kvmdfake.New(t)
	f.SetMouseAbsolute(false)
	js := `[
		{"type":"move","x":10,"y":20},
		{"type":"click","x":11,"y":21},
		{"type":"double_click","x":12,"y":22},
		{"type":"scroll","dy":-3,"dx":0},
		{"type":"nudge","dx":7,"dy":-8}
	]`
	out, stderr, code := runCLI(t, f, "run", "--actions-json", js, "-o", "json")
	if code != 0 || !strings.Contains(out, `"completed": 5`) {
		t.Fatalf("code %d out %s stderr %s", code, out, stderr)
	}
	if len(paths(f, "/api/hid/events/send_mouse_move")) != 3 {
		t.Fatalf("expected 3 mouse moves, got %v", paths(f, "/api/hid/events/send_mouse_move"))
	}
	if len(paths(f, "/api/hid/events/send_mouse_button")) != 3 {
		t.Fatalf("expected 3 button events (click + double_click x2), got %v", paths(f, "/api/hid/events/send_mouse_button"))
	}
	if len(paths(f, "/api/hid/events/send_mouse_wheel")) != 1 {
		t.Fatal("expected 1 scroll event")
	}
	got := paths(f, "/api/hid/events/send_mouse_relative")
	if len(got) != 1 || !strings.Contains(got[0], "delta_x=7") || !strings.Contains(got[0], "delta_y=-8") {
		t.Fatalf("nudge call wrong: %v", got)
	}
}

func TestRunScreenshotAction(t *testing.T) {
	f := kvmdfake.New(t)
	p := filepath.Join(t.TempDir(), "shot.jpg")
	js := `[{"type":"screenshot","file":"` + p + `"}]`
	out, stderr, code := runCLI(t, f, "run", "--actions-json", js, "-o", "json")
	if code != 0 || !strings.Contains(out, `"completed": 1`) {
		t.Fatalf("code %d out %s stderr %s", code, out, stderr)
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) < 2 || b[0] != 0xff || b[1] != 0xd8 {
		t.Fatalf("screenshot not written as jpeg: %v %v", err, b)
	}
}

func TestRunObserveAfter(t *testing.T) {
	f := kvmdfake.New(t)
	p := filepath.Join(t.TempDir(), "after.jpg")
	js := `[{"type":"wait","ms":1}]`
	out, stderr, code := runCLI(t, f, "run", "--actions-json", js, "--observe-after", "--file", p, "-o", "json")
	if code != 0 || !strings.Contains(out, `"screenshot"`) {
		t.Fatalf("code %d out %s stderr %s", code, out, stderr)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("observe-after screenshot not written: %v", err)
	}
}

func TestRunObserveAfterRequiresFile(t *testing.T) {
	f := kvmdfake.New(t)
	js := `[{"type":"wait","ms":1}]`
	_, stderr, code := runCLI(t, f, "run", "--actions-json", js, "--observe-after")
	if code != ExitUsage || !strings.Contains(stderr, "--observe-after") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if len(paths(f, "/api/hid/")) != 0 {
		t.Fatal("sent actions despite missing --file")
	}
}

func TestRunBatchTooLarge(t *testing.T) {
	f := kvmdfake.New(t)
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 101; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"type":"wait","ms":0}`)
	}
	b.WriteString("]")
	_, stderr, code := runCLI(t, f, "run", "--actions-json", b.String())
	if code != ExitUsage || !strings.Contains(stderr, "at most 100") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestRunWaitOutOfRange(t *testing.T) {
	f := kvmdfake.New(t)
	js := `[{"type":"wait","ms":70000}]`
	_, stderr, code := runCLI(t, f, "run", "--actions-json", js)
	if code != ExitUsage || !strings.Contains(stderr, "action 0") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestRunUnknownType(t *testing.T) {
	f := kvmdfake.New(t)
	js := `[{"type":"key","keys":"f13"},{"type":"bogus"}]`
	_, stderr, code := runCLI(t, f, "run", "--actions-json", js)
	if code != ExitUsage || !strings.Contains(stderr, "action 1") || !strings.Contains(stderr, "bogus") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if len(paths(f, "/api/hid/")) != 0 {
		t.Fatal("sent actions before validating the batch")
	}
}

func TestRunMissingRequiredField(t *testing.T) {
	f := kvmdfake.New(t)
	js := `[{"type":"move","x":5}]`
	_, stderr, code := runCLI(t, f, "run", "--actions-json", js)
	if code != ExitUsage || !strings.Contains(stderr, "action 0") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestRunInvalidButton(t *testing.T) {
	f := kvmdfake.New(t)
	js := `[{"type":"click","x":1,"y":1,"button":"bogus"}]`
	_, stderr, code := runCLI(t, f, "run", "--actions-json", js)
	if code != ExitUsage || !strings.Contains(stderr, "action 0") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

// TestRunCoordinateOutOfBoundsAtExecution checks that an out-of-bounds pixel
// coordinate, which cannot be validated before the capture size is known,
// still stops the batch and exits like a device failure, not a usage error,
// once caught at execution time.
func TestRunCoordinateOutOfBoundsAtExecution(t *testing.T) {
	f := kvmdfake.New(t) // default capture is 1200x752
	js := `[{"type":"move","x":9999,"y":10}]`
	out, _, code := runCLI(t, f, "run", "--actions-json", js, "-o", "json")
	if code != ExitDevice || !strings.Contains(out, `"completed": 0`) {
		t.Fatalf("code %d out %s", code, out)
	}
	if len(paths(f, "/api/hid/events/send_mouse_move")) != 0 {
		t.Fatal("out of bounds move reached the device")
	}
}

func TestRunActionsFile(t *testing.T) {
	f := kvmdfake.New(t)
	p := filepath.Join(t.TempDir(), "actions.json")
	if err := os.WriteFile(p, []byte(`[{"type":"key","keys":"f13"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runCLI(t, f, "run", "--actions-file", p, "-o", "json")
	if code != 0 || !strings.Contains(out, `"completed": 1`) {
		t.Fatalf("code %d out %s stderr %s", code, out, stderr)
	}
}

func TestRunActionsJSONAndFileMutuallyExclusive(t *testing.T) {
	f := kvmdfake.New(t)
	p := filepath.Join(t.TempDir(), "actions.json")
	_ = os.WriteFile(p, []byte(`[]`), 0o600)
	_, stderr, code := runCLI(t, f, "run", "--actions-json", "[]", "--actions-file", p)
	if code != ExitUsage || !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

// TestBatchStatePointerSizeReResolvesAfterPort checks that a "port" action
// invalidates the cached capture size, so the next pointer action resolves
// it again instead of reusing a possibly stale value from a different host.
func TestBatchStatePointerSizeReResolvesAfterPort(t *testing.T) {
	f := kvmdfake.New(t)
	c := kvmd.New(f.Device(), 5*time.Second)
	s := &batchState{ctx: context.Background(), c: c}

	if _, _, err := s.pointerSize(); err != nil {
		t.Fatal(err)
	}
	if !s.sizeKnown {
		t.Fatal("size not cached after first resolve")
	}
	firstStreamerCalls := len(paths(f, "/api/streamer"))

	if _, _, err := s.pointerSize(); err != nil {
		t.Fatal(err)
	}
	if len(paths(f, "/api/streamer")) != firstStreamerCalls {
		t.Fatal("pointerSize re-resolved despite a cached size")
	}

	n := 2
	if err := s.execute(rawAction{Type: "port", Port: &n}); err != nil {
		t.Fatal(err)
	}
	if s.sizeKnown {
		t.Fatal("port action did not invalidate the cached size")
	}
	afterPortCalls := len(paths(f, "/api/streamer"))

	if _, _, err := s.pointerSize(); err != nil {
		t.Fatal(err)
	}
	if len(paths(f, "/api/streamer")) <= afterPortCalls {
		t.Fatal("pointerSize did not re-resolve after port invalidated the cache")
	}
}
