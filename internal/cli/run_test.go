package cli

import (
	"context"
	"encoding/json"
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

// TestRunEnvelopeSuccess pins the success envelope shape to the same global
// {"ok":true,"result":{...}} envelope every other command uses.
func TestRunEnvelopeSuccess(t *testing.T) {
	f := kvmdfake.New(t)
	js := `[{"type":"key","keys":"f13"}]`
	out, stderr, code := runCLI(t, f, "run", "--actions-json", js, "-o", "json")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Completed int       `json:"completed"`
			Total     int       `json:"total"`
			Receipts  []receipt `json:"receipts"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if !env.OK || env.Result.Completed != 1 || env.Result.Total != 1 || len(env.Result.Receipts) != 1 {
		t.Fatalf("out %s", out)
	}
	if strings.Contains(out, `"error"`) {
		t.Fatalf("success envelope should not carry an error field: %s", out)
	}
}

// TestRunEnvelopeFailure pins the failure envelope shape:
// {"ok":false,"error":{"kind":"device","message":"..."},"result":{...}},
// exit 1, with the result's receipts still present.
func TestRunEnvelopeFailure(t *testing.T) {
	f := kvmdfake.New(t)
	f.FailNext["/api/hid/print"] = kvmdfake.Failure{Status: 500, Kind: "HidError", Msg: "boom"}
	js := `[{"type":"key","keys":"f13"},{"type":"type","text":"x"}]`
	out, _, code := runCLI(t, f, "run", "--actions-json", js, "-o", "json")
	if code != ExitDevice {
		t.Fatalf("code %d out %s", code, out)
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			Completed int       `json:"completed"`
			Total     int       `json:"total"`
			Receipts  []receipt `json:"receipts"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if env.OK {
		t.Fatalf("expected ok:false, out %s", out)
	}
	if env.Error.Kind != "device" || env.Error.Message == "" {
		t.Fatalf("bad error envelope: %+v out %s", env.Error, out)
	}
	if env.Result.Completed != 1 || env.Result.Total != 2 || len(env.Result.Receipts) != 2 {
		t.Fatalf("bad result envelope: %+v out %s", env.Result, out)
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
	out, stderr, code := runCLI(t, f, "run", "--actions-json", js, "--observe-after", "--file", p, "--observe-delay", "1ms", "-o", "json")
	if code != 0 || !strings.Contains(out, `"screenshot"`) {
		t.Fatalf("code %d out %s stderr %s", code, out, stderr)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("observe-after screenshot not written: %v", err)
	}
}

// TestRunObserveDelayFlagSleeps checks that --observe-delay is honored: a
// small explicit delay measurably slows the command down. Kept tiny to
// avoid a slow test.
func TestRunObserveDelayFlagSleeps(t *testing.T) {
	f := kvmdfake.New(t)
	p := filepath.Join(t.TempDir(), "after.jpg")
	js := `[{"type":"wait","ms":1}]`
	start := time.Now()
	_, stderr, code := runCLI(t, f, "run", "--actions-json", js, "--observe-after", "--file", p, "--observe-delay", "30ms")
	elapsed := time.Since(start)
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if elapsed < 30*time.Millisecond {
		t.Fatalf("--observe-delay 30ms did not sleep: elapsed %s", elapsed)
	}
}

// TestRunObserveDelayZeroDisablesSleep checks that --observe-delay 0 skips
// the wait entirely. Asserts a generous upper bound well under the 300ms
// default, so a regression that ignores "0" would fail this quickly rather
// than the test itself being slow.
func TestRunObserveDelayZeroDisablesSleep(t *testing.T) {
	f := kvmdfake.New(t)
	p := filepath.Join(t.TempDir(), "after.jpg")
	js := `[{"type":"wait","ms":1}]`
	start := time.Now()
	_, stderr, code := runCLI(t, f, "run", "--actions-json", js, "--observe-after", "--file", p, "--observe-delay", "0")
	elapsed := time.Since(start)
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("--observe-delay 0 should disable the wait, elapsed %s", elapsed)
	}
}

// TestRunObserveAfterSkippedOnFailure checks requirement: when an action
// fails, the batch stops before --observe-after ever takes its screenshot,
// so no snapshot call reaches the device after the failure.
func TestRunObserveAfterSkippedOnFailure(t *testing.T) {
	f := kvmdfake.New(t)
	f.FailNext["/api/hid/print"] = kvmdfake.Failure{Status: 500, Kind: "HidError", Msg: "boom"}
	p := filepath.Join(t.TempDir(), "after.jpg")
	js := `[{"type":"type","text":"x"}]`
	_, _, code := runCLI(t, f, "run", "--actions-json", js, "--observe-after", "--file", p, "--observe-delay", "0", "-o", "json")
	if code != ExitDevice {
		t.Fatalf("code %d", code)
	}
	if len(paths(f, "/api/streamer/snapshot")) != 0 {
		t.Fatal("--observe-after took a screenshot despite the batch failing")
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("--observe-after wrote a file despite the batch failing")
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

// TestRunUnknownActionFieldIsUsageError pins fix round 3's finding 5: an
// unknown field in an action (e.g. a typo'd "buton" instead of "button")
// used to be silently ignored, sending a left click instead of the
// requested right one. It must be a usage error naming the action index
// instead, before any action in the batch is sent.
func TestRunUnknownActionFieldIsUsageError(t *testing.T) {
	f := kvmdfake.New(t)
	js := `[{"type":"click","x":1,"y":1,"buton":"right"}]`
	_, stderr, code := runCLI(t, f, "run", "--actions-json", js)
	if code != ExitUsage || !strings.Contains(stderr, "action 0") || !strings.Contains(stderr, "buton") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if len(paths(f, "/api/hid/")) != 0 {
		t.Fatal("sent actions despite an unknown field")
	}
}

// TestRunMissingActionsFileIsUsageError pins fix round 3's finding 10: a
// missing --actions-file is a usage error (exit 2), not a device error.
func TestRunMissingActionsFileIsUsageError(t *testing.T) {
	f := kvmdfake.New(t)
	missing := filepath.Join(t.TempDir(), "nope.json")
	_, stderr, code := runCLI(t, f, "run", "--actions-file", missing)
	if code != ExitUsage {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

// TestRunObserveAfterScreenshotFailureReportsBatchCompleted pins fix round
// 3's finding 7: when the batch itself succeeds but the --observe-after
// screenshot fails, the error message must say so explicitly instead of
// reading like nothing ran.
func TestRunObserveAfterScreenshotFailureReportsBatchCompleted(t *testing.T) {
	f := kvmdfake.New(t)
	f.SetSource(false, "no_signal", 0, 0)
	p := filepath.Join(t.TempDir(), "after.jpg")
	js := `[{"type":"key","keys":"f13"}]`
	_, stderr, code := runCLI(t, f, "run", "--actions-json", js, "--observe-after", "--file", p, "--observe-delay", "0")
	if code != ExitDevice {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if !strings.Contains(stderr, "batch completed (1/1 actions)") || !strings.Contains(stderr, "observe-after screenshot failed") {
		t.Fatalf("stderr %s", stderr)
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
