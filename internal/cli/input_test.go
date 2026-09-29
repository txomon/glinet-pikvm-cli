package cli

import (
	"strings"
	"testing"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func paths(f *kvmdfake.Server, prefix string) []string {
	var out []string
	for _, c := range f.Calls() {
		if strings.HasPrefix(c.Path, prefix) {
			out = append(out, c.Path+"?"+c.Query.Encode())
		}
	}
	return out
}

func TestKeyCombo(t *testing.T) {
	f := kvmdfake.New(t)
	if _, e, code := runCLI(t, f, "key", "ctrl+alt+del", "f13"); code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	got := paths(f, "/api/hid/events/send_")
	if len(got) != 2 || !strings.Contains(got[0], "send_shortcut?keys=ControlLeft%2CAltLeft%2CDelete") || !strings.Contains(got[1], "send_key?key=F13") {
		t.Fatalf("%v", got)
	}
}

func TestKeyUnknown(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "key", "ctrl+bogus")
	if code != ExitUsage || !strings.Contains(stderr, "bogus") {
		t.Fatalf("code %d %s", code, stderr)
	}
	if len(paths(f, "/api/hid/")) != 0 {
		t.Fatal("bad combo reached the device")
	}
}

func TestKeyMultipleCombosValidateBeforeSending(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "key", "a", "ctrl+bogus")
	if code != ExitUsage || !strings.Contains(stderr, "bogus") {
		t.Fatalf("code %d %s", code, stderr)
	}
	if len(paths(f, "/api/hid/")) != 0 {
		t.Fatal("first (valid) combo reached the device before the second failed to parse")
	}
}

func TestKeyHold(t *testing.T) {
	f := kvmdfake.New(t)
	_, e, code := runCLI(t, f, "key", "--hold", "10ms", "ctrl+alt+del")
	if code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	calls := f.Calls()
	if len(calls) != 6 {
		t.Fatalf("want 6 calls (3 press + 3 release), got %d: %+v", len(calls), calls)
	}
	wantKey := []string{"ControlLeft", "AltLeft", "Delete", "Delete", "AltLeft", "ControlLeft"}
	wantState := []string{"true", "true", "true", "false", "false", "false"}
	for i, c := range calls {
		if c.Path != "/api/hid/events/send_key" || c.Query.Get("key") != wantKey[i] || c.Query.Get("state") != wantState[i] {
			t.Fatalf("call %d: %+v (want key=%s state=%s)", i, c, wantKey[i], wantState[i])
		}
	}
}

func TestKeyHoldPressFailure(t *testing.T) {
	f := kvmdfake.New(t)
	f.FailNext["/api/hid/events/send_key"] = kvmdfake.Failure{Status: 400, Kind: "ValidatorError", Msg: "boom"}
	_, stderr, code := runCLI(t, f, "key", "--hold", "10ms", "ctrl+alt+del")
	if code != ExitDevice || !strings.Contains(stderr, "boom") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 call (the failed first press, nothing to release yet), got %d: %+v", len(calls), calls)
	}
}

func TestMouseClickScales(t *testing.T) {
	f := kvmdfake.New(t) // 1200x752
	if _, e, code := runCLI(t, f, "mouse", "click", "0", "751"); code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	got := paths(f, "/api/hid/events/send_mouse_")
	if len(got) != 2 || !strings.Contains(got[0], "to_x=-32768") || !strings.Contains(got[0], "to_y=32767") || !strings.Contains(got[1], "button=left") {
		t.Fatalf("%v", got)
	}
}

func TestMouseClickButtonFlag(t *testing.T) {
	f := kvmdfake.New(t)
	if _, e, code := runCLI(t, f, "mouse", "click", "10", "10", "--button", "right"); code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	got := paths(f, "/api/hid/events/send_mouse_button")
	if len(got) != 1 || !strings.Contains(got[0], "button=right") {
		t.Fatalf("%v", got)
	}
}

func TestMouseClickBadButton(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "mouse", "click", "10", "10", "--button", "sideways")
	if code != ExitUsage || !strings.Contains(stderr, "sideways") {
		t.Fatalf("code %d %s", code, stderr)
	}
	if len(paths(f, "/api/hid/")) != 0 {
		t.Fatal("bad button reached the device")
	}
}

func TestMouseDoubleClick(t *testing.T) {
	f := kvmdfake.New(t)
	if _, e, code := runCLI(t, f, "mouse", "double-click", "100", "100"); code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	moves := paths(f, "/api/hid/events/send_mouse_move")
	buttons := paths(f, "/api/hid/events/send_mouse_button")
	if len(moves) != 1 {
		t.Fatalf("want 1 move, got %v", moves)
	}
	if len(buttons) != 2 || !strings.Contains(buttons[0], "button=left") || !strings.Contains(buttons[1], "button=left") {
		t.Fatalf("want 2 left button calls, got %v", buttons)
	}
}

func TestMouseOutOfBounds(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "mouse", "click", "1200", "10")
	if code != ExitUsage || !strings.Contains(stderr, "1200x752") {
		t.Fatalf("code %d %s", code, stderr)
	}
}

func TestMouseNegativeOutOfBounds(t *testing.T) {
	f := kvmdfake.New(t)
	// "--" is required before a negative positional argument: pflag always
	// treats a bare "-1" as an attempted flag, never as a value, unless flag
	// parsing has already been terminated.
	_, stderr, code := runCLI(t, f, "mouse", "move", "--", "-1", "10")
	if code != ExitUsage || !strings.Contains(stderr, "1200x752") {
		t.Fatalf("code %d %s", code, stderr)
	}
}

func TestMouseNoSignal(t *testing.T) {
	f := kvmdfake.New(t)
	f.SetSource(false, "no signal", 0, 0)
	_, stderr, code := runCLI(t, f, "mouse", "move", "10", "10")
	if code != ExitDevice || !strings.Contains(stderr, "capture signal") {
		t.Fatalf("code %d %s", code, stderr)
	}
}

func TestMouseSizeOverrideSkipsStreamerLookup(t *testing.T) {
	f := kvmdfake.New(t)
	f.SetSource(false, "no signal", 0, 0)
	_, e, code := runCLI(t, f, "mouse", "move", "10", "10", "--size", "100x100")
	if code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	got := paths(f, "/api/hid/events/send_mouse_move")
	if len(got) != 1 {
		t.Fatalf("%v", got)
	}
}

func TestMouseDragDefaultSteps(t *testing.T) {
	f := kvmdfake.New(t) // 1200x752
	if _, e, code := runCLI(t, f, "mouse", "drag", "0", "0", "1199", "751"); code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	moves := paths(f, "/api/hid/events/send_mouse_move")
	buttons := paths(f, "/api/hid/events/send_mouse_button")
	// 1 initial move to the start point, plus defaultDragSteps (10) steps to the end.
	if len(moves) != 11 {
		t.Fatalf("want 11 moves, got %v", moves)
	}
	if !strings.Contains(moves[0], "to_x=-32768") || !strings.Contains(moves[0], "to_y=-32768") {
		t.Fatalf("first move should be the start point: %v", moves[0])
	}
	if !strings.Contains(moves[len(moves)-1], "to_x=32767") || !strings.Contains(moves[len(moves)-1], "to_y=32767") {
		t.Fatalf("last move should be the end point: %v", moves[len(moves)-1])
	}
	if len(buttons) != 2 || !strings.Contains(buttons[0], "button=left") || !strings.Contains(buttons[0], "state=true") ||
		!strings.Contains(buttons[1], "button=left") || !strings.Contains(buttons[1], "state=false") {
		t.Fatalf("want press then release, got %v", buttons)
	}
}

func TestMouseDragCustomSteps(t *testing.T) {
	f := kvmdfake.New(t)
	if _, e, code := runCLI(t, f, "mouse", "drag", "0", "0", "100", "100", "--steps", "2"); code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	moves := paths(f, "/api/hid/events/send_mouse_move")
	if len(moves) != 3 {
		t.Fatalf("want 1 start move + 2 steps, got %v", moves)
	}
}

func TestMouseDragBadSteps(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "mouse", "drag", "0", "0", "10", "10", "--steps", "0")
	if code != ExitUsage || !strings.Contains(stderr, "steps") {
		t.Fatalf("code %d %s", code, stderr)
	}
	if len(paths(f, "/api/hid/")) != 0 {
		t.Fatal("bad steps reached the device")
	}
}

func TestMouseDragPressFailure(t *testing.T) {
	f := kvmdfake.New(t)
	f.FailNext["/api/hid/events/send_mouse_button"] = kvmdfake.Failure{Status: 400, Kind: "ValidatorError", Msg: "boom"}
	_, stderr, code := runCLI(t, f, "mouse", "drag", "0", "0", "100", "100")
	if code != ExitDevice || !strings.Contains(stderr, "boom") {
		t.Fatalf("code %d %s", code, stderr)
	}
	moves := paths(f, "/api/hid/events/send_mouse_move")
	buttons := paths(f, "/api/hid/events/send_mouse_button")
	if len(moves) != 1 || len(buttons) != 1 {
		t.Fatalf("want just the initial move and the failed press, got moves=%v buttons=%v", moves, buttons)
	}
}

func TestMouseScroll(t *testing.T) {
	f := kvmdfake.New(t)
	// The flag must come before "--" so pflag still parses it as a flag; "--"
	// then lets the negative DY through as a positional argument.
	if _, e, code := runCLI(t, f, "mouse", "scroll", "--dx", "3", "--", "-5"); code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	got := paths(f, "/api/hid/events/send_mouse_wheel")
	if len(got) != 1 || !strings.Contains(got[0], "delta_x=3") || !strings.Contains(got[0], "delta_y=-5") {
		t.Fatalf("%v", got)
	}
}

func TestMouseScrollOutOfRange(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "mouse", "scroll", "200")
	if code != ExitUsage || !strings.Contains(stderr, "-127") {
		t.Fatalf("code %d %s", code, stderr)
	}
	if len(paths(f, "/api/hid/")) != 0 {
		t.Fatal("out of range delta reached the device")
	}
}

func TestMouseNudge(t *testing.T) {
	f := kvmdfake.New(t)
	if _, e, code := runCLI(t, f, "mouse", "nudge", "--", "7", "-8"); code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	got := paths(f, "/api/hid/events/send_mouse_relative")
	if len(got) != 1 || !strings.Contains(got[0], "delta_x=7") || !strings.Contains(got[0], "delta_y=-8") {
		t.Fatalf("%v", got)
	}
}

func TestTypeStdin(t *testing.T) {
	f := kvmdfake.New(t)
	if _, e, code := runCLI(t, f, "type", "abc"); code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	calls := f.Calls()
	last := calls[len(calls)-1]
	if last.Path != "/api/hid/print" || string(last.Body) != "abc" {
		t.Fatalf("%+v", last)
	}
}

func TestTypeFlags(t *testing.T) {
	f := kvmdfake.New(t)
	if _, e, code := runCLI(t, f, "type", "hi", "--slow", "--keymap", "de-de"); code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	calls := f.Calls()
	last := calls[len(calls)-1]
	if last.Query.Get("slow") != "true" || last.Query.Get("keymap") != "de-de" {
		t.Fatalf("%+v", last)
	}
}

func TestTypeArgsValidation(t *testing.T) {
	f := kvmdfake.New(t)
	if _, stderr, code := runCLI(t, f, "type"); code != ExitUsage {
		t.Fatalf("no args: code %d %s", code, stderr)
	}
	if _, stderr, code := runCLI(t, f, "type", "--stdin", "abc"); code != ExitUsage {
		t.Fatalf("stdin + arg: code %d %s", code, stderr)
	}
}

func TestActionWithFileReportsScreenshot(t *testing.T) {
	f := kvmdfake.New(t)
	p := t.TempDir() + "/s.jpg"
	out, e, code := runCLI(t, f, "type", "abc", "--file", p, "-o", "json")
	if code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	if !strings.Contains(out, `"actions": 1`) || !strings.Contains(out, `"screenshot"`) || !strings.Contains(out, `"width": 1200`) {
		t.Fatalf("out %s", out)
	}
}

func TestActionWithoutFileHasNoScreenshotKey(t *testing.T) {
	f := kvmdfake.New(t)
	out, e, code := runCLI(t, f, "type", "abc", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	if !strings.Contains(out, `"actions": 1`) || strings.Contains(out, "screenshot") {
		t.Fatalf("out %s", out)
	}
}

func TestKeyActionsCountsCombos(t *testing.T) {
	f := kvmdfake.New(t)
	out, e, code := runCLI(t, f, "key", "a", "b", "c", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d %s", code, e)
	}
	if !strings.Contains(out, `"actions": 3`) {
		t.Fatalf("out %s", out)
	}
}
