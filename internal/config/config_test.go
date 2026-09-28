package config

import (
	"errors"
	"os"
	"path/filepath"
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
