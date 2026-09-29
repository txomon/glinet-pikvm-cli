package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

func TestMSDFlow(t *testing.T) {
	f := kvmdfake.New(t)
	p := filepath.Join(t.TempDir(), "tiny.img")
	_ = os.WriteFile(p, bytes.Repeat([]byte{1}, 8192), 0o600)
	steps := [][]string{
		{"msd", "upload", p},
		{"msd", "attach", "tiny.img", "--flash"},
	}
	for _, s := range steps {
		if _, e, code := runCLI(t, f, s...); code != 0 {
			t.Fatalf("%v: code %d %s", s, code, e)
		}
	}
	out, _, _ := runCLI(t, f, "msd", "-o", "json")
	if !strings.Contains(out, `"connected": true`) || !strings.Contains(out, `"tiny.img"`) {
		t.Fatalf("%s", out)
	}
	if _, stderr, code := runCLI(t, f, "msd", "remove", "tiny.img"); code != ExitUsage || !strings.Contains(stderr, "detach") {
		t.Fatalf("remove while attached: code %d %s", code, stderr)
	}
	for _, s := range [][]string{{"msd", "detach"}, {"msd", "remove", "tiny.img"}} {
		if _, e, code := runCLI(t, f, s...); code != 0 {
			t.Fatalf("%v: code %d %s", s, code, e)
		}
	}
}

func TestMSDUploadDuplicate(t *testing.T) {
	f := kvmdfake.New(t)
	p := filepath.Join(t.TempDir(), "a.img")
	_ = os.WriteFile(p, []byte("x"), 0o600)
	runCLI(t, f, "msd", "upload", p)
	if _, _, code := runCLI(t, f, "msd", "upload", p); code != ExitUsage {
		t.Fatalf("duplicate upload code %d", code)
	}
}

func TestMSDRWNeedsFlash(t *testing.T) {
	if _, _, code := runCLI(t, kvmdfake.New(t), "msd", "attach", "x.iso", "--rw"); code != ExitUsage {
		t.Fatalf("code %d", code)
	}
}

// writeImage creates a small placeholder image file in the test's temp dir
// and returns its path.
func writeImage(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func otgCalls(f *kvmdfake.Server) []kvmdfake.Call {
	var out []kvmdfake.Call
	for _, c := range f.Calls() {
		if c.Path == "/api/system/otg_functions" {
			out = append(out, c)
		}
	}
	return out
}

func otgPOSTs(f *kvmdfake.Server) []kvmdfake.Call {
	var out []kvmdfake.Call
	for _, c := range otgCalls(f) {
		if c.Method == "POST" {
			out = append(out, c)
		}
	}
	return out
}

func TestMSDAttachTurnsStartCDROMOn(t *testing.T) {
	f := kvmdfake.New(t)
	p := writeImage(t, "a.img")
	if _, e, code := runCLI(t, f, "msd", "upload", p); code != 0 {
		t.Fatalf("upload: code %d %s", code, e)
	}

	if _, stderr, code := runCLI(t, f, "msd", "attach", "a.img"); code != 0 {
		t.Fatalf("attach: code %d %s", code, stderr)
	}

	if !f.OTG.StartCDROM {
		t.Fatal("start_cdrom still off after attach")
	}
	found := false
	for _, c := range otgCalls(f) {
		if c.Method == "POST" && c.Query.Get("start_cdrom") == "true" {
			found = true
		}
	}
	if !found {
		t.Fatal("no POST start_cdrom=true call")
	}
}

func TestMSDAttachAlreadyOnSkipsPOST(t *testing.T) {
	f := kvmdfake.New(t)
	f.OTG.StartCDROM = true
	p := writeImage(t, "a.img")
	if _, e, code := runCLI(t, f, "msd", "upload", p); code != 0 {
		t.Fatalf("upload: code %d %s", code, e)
	}

	if _, stderr, code := runCLI(t, f, "msd", "attach", "a.img"); code != 0 {
		t.Fatalf("attach: code %d %s", code, stderr)
	}

	for _, c := range otgCalls(f) {
		if c.Method == "POST" {
			t.Fatalf("unexpected POST to otg_functions when already on: %+v", c)
		}
	}
}

func TestMSDAttachApplyErrorFails(t *testing.T) {
	f := kvmdfake.New(t)
	p := writeImage(t, "a.img")
	if _, e, code := runCLI(t, f, "msd", "upload", p); code != 0 {
		t.Fatalf("upload: code %d %s", code, e)
	}
	f.FailOTGApply("gadget rebuild failed")

	_, stderr, code := runCLI(t, f, "msd", "attach", "a.img")
	if code == 0 || !strings.Contains(stderr, "gadget rebuild failed") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	for _, c := range f.Calls() {
		if c.Path == "/api/msd/set_params" {
			t.Fatal("set_params called despite failed otg apply")
		}
	}
}

func TestMSDDetachTurnsStartCDROMOff(t *testing.T) {
	f := kvmdfake.New(t)
	p := writeImage(t, "a.img")
	runCLI(t, f, "msd", "upload", p)
	if _, e, code := runCLI(t, f, "msd", "attach", "a.img"); code != 0 {
		t.Fatalf("attach: code %d %s", code, e)
	}

	if _, stderr, code := runCLI(t, f, "msd", "detach"); code != 0 {
		t.Fatalf("detach: code %d %s", code, stderr)
	}

	if f.OTG.StartCDROM {
		t.Fatal("start_cdrom still on after detach")
	}
	found := false
	for _, c := range otgCalls(f) {
		if c.Method == "POST" && c.Query.Get("start_cdrom") == "false" {
			found = true
		}
	}
	if !found {
		t.Fatal("no POST start_cdrom=false call")
	}
}

func TestMSDDetachKeepUSB(t *testing.T) {
	f := kvmdfake.New(t)
	p := writeImage(t, "a.img")
	runCLI(t, f, "msd", "upload", p)
	if _, e, code := runCLI(t, f, "msd", "attach", "a.img"); code != 0 {
		t.Fatalf("attach: code %d %s", code, e)
	}

	if _, stderr, code := runCLI(t, f, "msd", "detach", "--keep-usb"); code != 0 {
		t.Fatalf("detach: code %d %s", code, stderr)
	}

	if !f.OTG.StartCDROM {
		t.Fatal("start_cdrom turned off despite --keep-usb")
	}
	for _, c := range otgCalls(f) {
		if c.Method == "POST" && c.Query.Get("start_cdrom") == "false" {
			t.Fatalf("unexpected POST turning start_cdrom off with --keep-usb: %+v", c)
		}
	}
}

func TestMSDAttachDetachesDifferentImageFirst(t *testing.T) {
	f := kvmdfake.New(t)
	p1 := writeImage(t, "one.img")
	p2 := writeImage(t, "two.img")
	runCLI(t, f, "msd", "upload", p1)
	runCLI(t, f, "msd", "upload", p2)
	if _, e, code := runCLI(t, f, "msd", "attach", "one.img"); code != 0 {
		t.Fatalf("attach one: code %d %s", code, e)
	}

	out, stderr, code := runCLI(t, f, "msd", "attach", "two.img", "-o", "json")
	if code != 0 {
		t.Fatalf("attach two: code %d %s", code, stderr)
	}
	if !strings.Contains(out, `"image": "two.img"`) {
		t.Fatalf("out %s", out)
	}
}

func TestMSDAttachBadImageIsDeviceError(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "msd", "attach", "nope.img")
	if code != ExitDevice || !strings.Contains(stderr, "MsdUnknownImageError") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestMSDStatusShowsUSB(t *testing.T) {
	f := kvmdfake.New(t)
	out, _, code := runCLI(t, f, "msd", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d out %s", code, out)
	}
	if !strings.Contains(out, `"usb": {`) || !strings.Contains(out, `"start_cdrom": false`) || !strings.Contains(out, `"ready": true`) {
		t.Fatalf("out %s", out)
	}

	out, _, code = runCLI(t, f, "msd")
	if code != 0 || !strings.Contains(out, "usb: start_cdrom=false ready=true") {
		t.Fatalf("text out %q code %d", out, code)
	}
}

func TestMSDUploadNotEnoughFreeSpace(t *testing.T) {
	f := kvmdfake.New(t)
	f.MSD.Free = 0
	p := writeImage(t, "big.img")
	_, stderr, code := runCLI(t, f, "msd", "upload", p)
	if code != ExitUsage || !strings.Contains(stderr, "not enough free space") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	for _, c := range f.Calls() {
		if c.Path == "/api/msd/write" {
			t.Fatal("upload reached the device despite insufficient free space")
		}
	}
}

// TestMSDAttachRetriesAfterApplyError pins fix round 1's finding 1: once an
// apply has failed, start_cdrom already equals the target value, so without
// a retry ensureStartCDROM would keep reading the same stale apply_error
// back forever and every later attach would fail identically.
func TestMSDAttachRetriesAfterApplyError(t *testing.T) {
	f := kvmdfake.New(t)
	p := writeImage(t, "a.img")
	if _, e, code := runCLI(t, f, "msd", "upload", p); code != 0 {
		t.Fatalf("upload: code %d %s", code, e)
	}
	f.FailOTGApply("gadget rebuild failed")

	_, stderr, code := runCLI(t, f, "msd", "attach", "a.img")
	if code == 0 || !strings.Contains(stderr, "gadget rebuild failed") {
		t.Fatalf("first attach: code %d stderr %s", code, stderr)
	}
	firstPOSTs := len(otgPOSTs(f))

	// FailOTGApply is single-shot: the fake no longer fails the next apply.
	_, stderr, code = runCLI(t, f, "msd", "attach", "a.img")
	if code != 0 {
		t.Fatalf("second attach: code %d stderr %s", code, stderr)
	}
	if len(otgPOSTs(f)) <= firstPOSTs {
		t.Fatal("no new POST to otg_functions on retry after a stale apply_error")
	}
	if f.OTG.ApplyError != "" {
		t.Fatalf("apply_error still set after a successful retry: %q", f.OTG.ApplyError)
	}
}

func TestMSDUploadReplaceHappyPath(t *testing.T) {
	f := kvmdfake.New(t)
	p1 := writeImage(t, "a.img")
	if _, e, code := runCLI(t, f, "msd", "upload", p1); code != 0 {
		t.Fatalf("initial upload: code %d %s", code, e)
	}

	p2 := filepath.Join(t.TempDir(), "newer.img")
	if err := os.WriteFile(p2, []byte("yyyy"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, stderr, code := runCLI(t, f, "msd", "upload", p2, "--name", "a.img", "--replace", "-o", "json")
	if code != 0 {
		t.Fatalf("replace upload: code %d stderr %s", code, stderr)
	}
	if !strings.Contains(out, `"replaced": true`) || !strings.Contains(out, `"size": 4`) {
		t.Fatalf("out %s", out)
	}
}

func TestMSDUploadReplaceRefusesConnectedImage(t *testing.T) {
	f := kvmdfake.New(t)
	p := writeImage(t, "a.img")
	runCLI(t, f, "msd", "upload", p)
	if _, e, code := runCLI(t, f, "msd", "attach", "a.img"); code != 0 {
		t.Fatalf("attach: code %d %s", code, e)
	}

	p2 := filepath.Join(t.TempDir(), "newer.img")
	if err := os.WriteFile(p2, []byte("yyyy"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := len(f.Calls())
	_, stderr, code := runCLI(t, f, "msd", "upload", p2, "--name", "a.img", "--replace")
	if code != ExitUsage || !strings.Contains(stderr, "detach") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	for _, c := range f.Calls()[before:] {
		if c.Path == "/api/msd/remove" || c.Path == "/api/msd/write" {
			t.Fatalf("device modified despite refusal: %+v", c)
		}
	}
}

func TestMSDRemoveWaitsForDisappearance(t *testing.T) {
	f := kvmdfake.New(t)
	f.MSDRemoveDelayPolls = 2
	p := writeImage(t, "a.img")
	runCLI(t, f, "msd", "upload", p)

	_, stderr, code := runCLI(t, f, "msd", "remove", "a.img")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestMSDRemoveTimesOutWhenStorageNeverCatchesUp(t *testing.T) {
	f := kvmdfake.New(t)
	f.MSDRemoveDelayPolls = 1000
	p := writeImage(t, "a.img")
	runCLI(t, f, "msd", "upload", p)

	orig := msdConfirmTimeout
	msdConfirmTimeout = 300 * time.Millisecond
	defer func() { msdConfirmTimeout = orig }()

	_, stderr, code := runCLI(t, f, "msd", "remove", "a.img")
	if code == 0 || !strings.Contains(stderr, "still listed") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestMSDUploadWaitsForAppearance(t *testing.T) {
	f := kvmdfake.New(t)
	f.MSDWriteDelayPolls = 2
	p := writeImage(t, "a.img")
	_, stderr, code := runCLI(t, f, "msd", "upload", p)
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestMSDUploadTimesOutWhenStorageNeverCatchesUp(t *testing.T) {
	f := kvmdfake.New(t)
	f.MSDWriteDelayPolls = 1000
	p := writeImage(t, "a.img")

	orig := msdConfirmTimeout
	msdConfirmTimeout = 300 * time.Millisecond
	defer func() { msdConfirmTimeout = orig }()

	_, stderr, code := runCLI(t, f, "msd", "upload", p)
	if code == 0 || !strings.Contains(stderr, "did not appear complete") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

// TestMSDUploadReplaceWithDelayedRemoval pins fix round 2's finding: live,
// "msd upload --replace" over an existing name failed with kvmd's
// MsdImageExistsError because the replace path removed and wrote right
// away, while kvmd's storage view still listed the old image for about a
// second. --replace must wait for the removal to be confirmed gone (the
// same removeAndConfirm doMSDRemove uses) before writing the new image
// under that name.
func TestMSDUploadReplaceWithDelayedRemoval(t *testing.T) {
	f := kvmdfake.New(t)
	p1 := writeImage(t, "a.img")
	if _, e, code := runCLI(t, f, "msd", "upload", p1); code != 0 {
		t.Fatalf("initial upload: code %d %s", code, e)
	}

	f.MSDRemoveDelayPolls = 2
	p2 := filepath.Join(t.TempDir(), "newer.img")
	if err := os.WriteFile(p2, []byte("yyyy"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, stderr, code := runCLI(t, f, "msd", "upload", p2, "--name", "a.img", "--replace", "-o", "json")
	if code != 0 {
		t.Fatalf("replace upload: code %d stderr %s", code, stderr)
	}
	if !strings.Contains(out, `"replaced": true`) || !strings.Contains(out, `"size": 4`) {
		t.Fatalf("out %s", out)
	}
}

// TestMSDWriteClearsPendingRemove pins the fake-only half of fix round 2:
// routeMSDWrite must clear any msdPendingRemove entry for the name it
// writes. Without it, a name freed by a delayed removal and immediately
// reused by a write would have its brand new data deleted once the old
// removal's countdown reaches zero on a later, unrelated poll. This uses a
// raw kvmd.Client (not doMSDUpload/doMSDRemove) to bypass their
// removeAndConfirm wait deliberately: that wait already keeps the CLI from
// ever writing while a removal is still pending, so this test is only about
// the fake's own correctness for any caller that does not wait.
func TestMSDWriteClearsPendingRemove(t *testing.T) {
	ctx := context.Background()
	f := kvmdfake.New(t)
	c := kvmd.New(f.Device(), 5*time.Second)

	first := []byte("first")
	if err := c.MSDUpload(ctx, "a.img", bytes.NewReader(first), int64(len(first))); err != nil {
		t.Fatal(err)
	}

	f.MSDRemoveDelayPolls = 2
	if err := c.MSDRemove(ctx, "a.img"); err != nil {
		t.Fatal(err)
	}

	// Reuse the name right away, without waiting for the delayed removal to
	// actually take effect in the fake.
	second := []byte("second")
	if err := c.MSDUpload(ctx, "a.img", bytes.NewReader(second), int64(len(second))); err != nil {
		t.Fatal(err)
	}

	// Exhaust the old removal's countdown (2 polls) with a margin, and
	// confirm the freshly written image survives every one of them.
	for i := 0; i < 4; i++ {
		st, err := c.MSD(ctx)
		if err != nil {
			t.Fatal(err)
		}
		img, ok := st.Images["a.img"]
		if !ok || img.Size != int64(len(second)) {
			t.Fatalf("poll %d: image missing or wrong size, got %+v (present=%v)", i, img, ok)
		}
	}
}
