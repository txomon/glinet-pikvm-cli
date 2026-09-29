package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
