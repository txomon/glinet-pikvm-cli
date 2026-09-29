package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

const sample = `{"devices":{"arwen":{"url":"https://arwen.example","user":"admin","password":"pw","insecure_tls":true}},"default_device":"arwen"}`

func TestLoadAndDefaultDevice(t *testing.T) {
	f, err := Load(write(t, sample, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.Device("")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "arwen" || d.URL != "https://arwen.example" || d.User != "admin" || d.Password != "pw" || !d.InsecureTLS {
		t.Fatalf("got %+v", d)
	}
}

func TestPasswordEnvOverride(t *testing.T) {
	t.Setenv("GLKVM_PASSWORD", "fromenv")
	f, _ := Load(write(t, sample, 0o600))
	d, _ := f.Device("arwen")
	if d.Password != "fromenv" {
		t.Fatalf("password %q", d.Password)
	}
}

func TestUnknownDevice(t *testing.T) {
	f, _ := Load(write(t, sample, 0o600))
	if _, err := f.Device("nope"); !errors.Is(err, ErrConfig) {
		t.Fatalf("want ErrConfig, got %v", err)
	}
}

func TestMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("want ErrConfig, got %v", err)
	}
}

func TestBadJSON(t *testing.T) {
	_, err := Load(write(t, "{", 0o600))
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("want ErrConfig, got %v", err)
	}
}

func TestPermWarning(t *testing.T) {
	if w := PermWarning(write(t, sample, 0o644)); w == "" {
		t.Fatal("expected warning for 0644")
	}
	if w := PermWarning(write(t, sample, 0o600)); w != "" {
		t.Fatalf("unexpected warning %q", w)
	}
}

func TestDefaultPathEnv(t *testing.T) {
	t.Setenv("GLKVM_CONFIG", "/x/y.json")
	if DefaultPath() != "/x/y.json" {
		t.Fatal(DefaultPath())
	}
}

func TestDeviceMissingURL(t *testing.T) {
	f, _ := Load(write(t, `{"devices":{"a":{"user":"u","password":"p"}},"default_device":"a"}`, 0o600))
	if _, err := f.Device(""); !errors.Is(err, ErrConfig) {
		t.Fatalf("want ErrConfig for missing url, got %v", err)
	}
}

// writeSecret writes body to a file in a fresh temp dir and returns its path.
func writeSecret(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveFromFilesTrimsTrailingNewline(t *testing.T) {
	urlFile := writeSecret(t, "url", "https://arwen.example\n")
	userFile := writeSecret(t, "user", "root\r\n")
	pwFile := writeSecret(t, "pw", "hunter2\n")
	cfg := `{"devices":{"a":{"url_file":"` + urlFile + `","user_file":"` + userFile + `","password_file":"` + pwFile + `"}},"default_device":"a"}`
	f, err := Load(write(t, cfg, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.Device("")
	if err != nil {
		t.Fatal(err)
	}
	if d.URL != "https://arwen.example" || d.User != "root" || d.Password != "hunter2" {
		t.Fatalf("got %+v", d)
	}
}

func TestValueAndFileConflictRejectedAtLoad(t *testing.T) {
	for _, cfg := range []string{
		`{"devices":{"a":{"url":"https://x.example","url_file":"/x"}}}`,
		`{"devices":{"a":{"url":"https://x.example","user":"u","user_file":"/x"}}}`,
		`{"devices":{"a":{"url":"https://x.example","password":"p","password_file":"/x"}}}`,
	} {
		_, err := Load(write(t, cfg, 0o600))
		if !errors.Is(err, ErrConfig) {
			t.Fatalf("cfg %s: want ErrConfig, got %v", cfg, err)
		}
	}
}

func TestMissingFileAtResolveNamesPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cfg := `{"devices":{"a":{"url_file":"` + missing + `"}},"default_device":"a"}`
	f, err := Load(write(t, cfg, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Device("")
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("want ErrConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error %v does not name path %s", err, missing)
	}
	if !strings.Contains(err.Error(), "a") {
		t.Fatalf("error %v does not name device", err)
	}
}

func TestUserDefaultsToAdmin(t *testing.T) {
	f, _ := Load(write(t, `{"devices":{"a":{"url":"https://x.example"}},"default_device":"a"}`, 0o600))
	d, err := f.Device("")
	if err != nil {
		t.Fatal(err)
	}
	if d.User != "admin" {
		t.Fatalf("user %q, want admin", d.User)
	}
}

func TestPasswordEnvOverridesPasswordFile(t *testing.T) {
	pwFile := writeSecret(t, "pw", "fromfile")
	t.Setenv("GLKVM_PASSWORD", "fromenv")
	cfg := `{"devices":{"a":{"url":"https://x.example","password_file":"` + pwFile + `"}},"default_device":"a"}`
	f, _ := Load(write(t, cfg, 0o600))
	d, err := f.Device("")
	if err != nil {
		t.Fatal(err)
	}
	if d.Password != "fromenv" {
		t.Fatalf("password %q, want fromenv", d.Password)
	}
}

func TestLookupReturnsRawDeviceWithoutResolvingFiles(t *testing.T) {
	cfg := `{"devices":{"a":{"url_file":"/does/not/exist"}},"default_device":"a"}`
	f, _ := Load(write(t, cfg, 0o600))
	d, err := f.Lookup("")
	if err != nil {
		t.Fatal(err)
	}
	if d.URLFile != "/does/not/exist" || d.URL != "" {
		t.Fatalf("got %+v", d)
	}
}

// TestTrimTrailingNewline pins that a lone trailing "\r" (no following
// "\n") is left alone: it is only stripped as part of a trailing "\r\n".
func TestTrimTrailingNewline(t *testing.T) {
	cases := map[string]string{
		"value\n":   "value",
		"value\r\n": "value",
		"value\r":   "value\r",
		"value":     "value",
		"\n":        "",
		"\r\n":      "",
		"\r":        "\r",
		"":          "",
	}
	for in, want := range cases {
		if got := TrimTrailingNewline(in); got != want {
			t.Errorf("TrimTrailingNewline(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoadBothConsistentWithLoadAndLoadRaw pins that LoadBoth's typed and
// raw views describe the same file: both come from one read of the same
// bytes, so "config device show" (LoadBoth's only caller) can never mix a
// device resolved from one snapshot with a raw key presence check from a
// different one.
func TestLoadBothConsistentWithLoadAndLoadRaw(t *testing.T) {
	cfg := `{"devices":{"a":{"url":"https://a.example","insecure_tls":true}},"default_device":"a"}`
	p := write(t, cfg, 0o600)

	f, rf, err := LoadBoth(p)
	if err != nil {
		t.Fatal(err)
	}
	wantF, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	wantRF, err := LoadRaw(p)
	if err != nil {
		t.Fatal(err)
	}

	if f.DefaultDevice != wantF.DefaultDevice || len(f.Devices) != len(wantF.Devices) {
		t.Fatalf("File mismatch: %+v vs %+v", f, wantF)
	}
	d, ok := f.Devices["a"]
	if !ok || d.URL != "https://a.example" || !d.InsecureTLS {
		t.Fatalf("device %+v", d)
	}

	if len(rf.Devices) != len(wantRF.Devices) {
		t.Fatalf("RawFile mismatch: %+v vs %+v", rf, wantRF)
	}
	if _, ok := rf.Devices["a"]["insecure_tls"]; !ok {
		t.Fatal("insecure_tls key missing from raw view")
	}
}

func TestLoadBothMissingFileIsError(t *testing.T) {
	_, _, err := LoadBoth(filepath.Join(t.TempDir(), "absent.json"))
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("want ErrConfig, got %v", err)
	}
}
