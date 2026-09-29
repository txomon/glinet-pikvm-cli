package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runConfig runs glkvm against configPath with no device contact, feeding
// stdin as the given body. configPath need not exist yet: "config device
// create" and "config default" both create it and its directory on demand.
func runConfig(t *testing.T, configPath, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb strings.Builder
	code := Execute(append([]string{"--config", configPath}, args...), strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), code
}

func freshConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.json")
}

func readConfigFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("bad json in %s: %v\n%s", path, err, b)
	}
	return m
}

func deviceFromConfigFile(t *testing.T, path, name string) map[string]any {
	t.Helper()
	m := readConfigFile(t, path)
	devices, ok := m["devices"].(map[string]any)
	if !ok {
		t.Fatalf("no devices object in %s: %+v", path, m)
	}
	d, ok := devices[name].(map[string]any)
	if !ok {
		t.Fatalf("no device %q in %s: %+v", name, path, devices)
	}
	return d
}

// --- config path ---

func TestConfigPath(t *testing.T) {
	p := freshConfigPath(t)
	out, _, code := runConfig(t, p, "", "config", "path")
	if code != 0 || strings.TrimSpace(out) != p {
		t.Fatalf("code %d out %q, want %q", code, out, p)
	}
}

// --- device create ---

func TestConfigDeviceCreate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.json")
	_, stderr, code := runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o, want 0600", st.Mode().Perm())
	}
	dst, err := os.Stat(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o, want 0700", dst.Mode().Perm())
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	if dev["url"] != "https://arwen.example.net" {
		t.Fatalf("device %+v", dev)
	}
}

func TestConfigDeviceCreateRefusesExistingName(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net")
	_, stderr, code := runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://other.example.net")
	if code != ExitUsage || !strings.Contains(stderr, "arwen") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	if dev["url"] != "https://arwen.example.net" {
		t.Fatalf("device was overwritten: %+v", dev)
	}
}

func TestConfigDeviceCreateReplace(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--user", "root")
	_, stderr, code := runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://other.example.net", "--replace")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	if dev["url"] != "https://other.example.net" {
		t.Fatalf("device %+v", dev)
	}
	if _, ok := dev["user"]; ok {
		t.Fatalf("replace should drop unknown/old keys not re-given: %+v", dev)
	}
}

func TestConfigDeviceCreateDefault(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--default")
	m := readConfigFile(t, p)
	if m["default_device"] != "arwen" {
		t.Fatalf("default_device %+v", m["default_device"])
	}
}

func TestConfigDeviceCreateRequiresExactlyOneURLSource(t *testing.T) {
	p := freshConfigPath(t)
	_, _, code := runConfig(t, p, "", "config", "device", "create", "arwen")
	if code != ExitUsage {
		t.Fatalf("no url: code %d", code)
	}
	_, _, code = runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://a.example.net", "--url-file", "/x")
	if code != ExitUsage {
		t.Fatalf("both url flags: code %d", code)
	}
}

func TestConfigDeviceCreateInvalidURLRefused(t *testing.T) {
	p := freshConfigPath(t)
	for _, bad := range []string{"not-a-url", "ftp://x.example.net", "http://", ""} {
		_, stderr, code := runConfig(t, p, "", "config", "device", "create", "arwen", "--url", bad)
		if code != ExitUsage {
			t.Fatalf("%q: code %d stderr %s", bad, code, stderr)
		}
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("config file should not have been created")
	}
}

// TestConfigDeviceCreateEmptyURLIsNotMisroutedToURLFile pins a fixed bug: an
// empty --url value (Changed("url") true, empty string) used to dispatch on
// the value being non-empty rather than on the flag being given, so an
// empty --url silently fell through to the url_file branch (storing an
// empty url_file) instead of being refused by validateDeviceURL.
func TestConfigDeviceCreateEmptyURLIsNotMisroutedToURLFile(t *testing.T) {
	p := freshConfigPath(t)
	_, stderr, code := runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "")
	if code != ExitUsage {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("config file should not have been created")
	}
}

func TestConfigDeviceSetEmptyURLRefused(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net")
	_, stderr, code := runConfig(t, p, "", "config", "device", "set", "arwen", "--url", "")
	if code != ExitUsage {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	if dev["url"] != "https://arwen.example.net" {
		t.Fatalf("device changed despite refusal: %+v", dev)
	}
}

func TestConfigDeviceCreateURLFileStoredAbsolute(t *testing.T) {
	p := freshConfigPath(t)
	_, stderr, code := runConfig(t, p, "", "config", "device", "create", "arwen", "--url-file", "relative-path")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	got, _ := dev["url_file"].(string)
	if !filepath.IsAbs(got) || !strings.HasSuffix(got, "relative-path") {
		t.Fatalf("url_file %q not made absolute", got)
	}
}

// TestConfigDeviceCreateSymlinkedConfigReadOnlyTargetExitsConfigError is an
// end-to-end check, through the full command path, that a config path
// symlinked into a read-only location fails as exit 3 (config error), not
// by silently replacing the symlink with a plain file. internal/config's
// own tests cover WithLocked's symlink handling in more detail; this just
// proves the CLI wiring reaches the same behavior.
func TestConfigDeviceCreateSymlinkedConfigReadOnlyTargetExitsConfigError(t *testing.T) {
	dir := t.TempDir()
	roDir := filepath.Join(dir, "ro")
	if err := os.Mkdir(roDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(roDir, "real-config.json")
	if err := os.WriteFile(target, []byte(`{"devices":{},"default_device":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o700) })

	linkPath := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runConfig(t, linkPath, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net")
	if code != ExitConfig {
		t.Fatalf("code %d stderr %s", code, stderr)
	}

	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced despite the failed write")
	}
}

func TestConfigDeviceCreatePasswordStdin(t *testing.T) {
	p := freshConfigPath(t)
	out, stderr, code := runConfig(t, p, "hunter2\n", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--password-stdin")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if strings.Contains(out, "hunter2") {
		t.Fatalf("password leaked into output: %s", out)
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	if dev["password"] != "hunter2" {
		t.Fatalf("device %+v", dev)
	}
}

func TestConfigDeviceCreatePasswordStdinRefusesEmpty(t *testing.T) {
	p := freshConfigPath(t)
	_, stderr, code := runConfig(t, p, "\n", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--password-stdin")
	if code != ExitUsage {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("config file should not have been created")
	}
}

func TestConfigDeviceCreatePasswordFlagsAreMutuallyExclusive(t *testing.T) {
	p := freshConfigPath(t)
	_, _, code := runConfig(t, p, "x\n", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--password-file", "/x", "--password-stdin")
	if code != ExitUsage {
		t.Fatalf("code %d", code)
	}
}

// TestConfigDeviceCreateEmptyFlagValuesRefused pins that an explicitly
// empty value for a flag that takes a path or a name is a usage error, not
// a silently stored empty string: "--url-file ”" used to fall through to
// storing url_file: "" instead of being refused.
func TestConfigDeviceCreateEmptyFlagValuesRefused(t *testing.T) {
	for _, args := range [][]string{
		{"config", "device", "create", "arwen", "--url-file", ""},
		{"config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--user", ""},
		{"config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--user-file", ""},
		{"config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--password-file", ""},
	} {
		p := freshConfigPath(t)
		_, stderr, code := runConfig(t, p, "", args...)
		if code != ExitUsage {
			t.Fatalf("%v: code %d stderr %s", args, code, stderr)
		}
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%v: config file should not have been created", args)
		}
	}
}

// TestConfigDeviceSetEmptyFlagValuesRefused is
// TestConfigDeviceCreateEmptyFlagValuesRefused's "set" counterpart, against
// an already-existing device so the empty value is the only thing under
// test.
func TestConfigDeviceSetEmptyFlagValuesRefused(t *testing.T) {
	for _, args := range [][]string{
		{"config", "device", "set", "arwen", "--url-file", ""},
		{"config", "device", "set", "arwen", "--user", ""},
		{"config", "device", "set", "arwen", "--user-file", ""},
		{"config", "device", "set", "arwen", "--password-file", ""},
	} {
		p := freshConfigPath(t)
		runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net")
		before := deviceFromConfigFile(t, p, "arwen")

		_, stderr, code := runConfig(t, p, "", args...)
		if code != ExitUsage {
			t.Fatalf("%v: code %d stderr %s", args, code, stderr)
		}
		after := deviceFromConfigFile(t, p, "arwen")
		if before["url"] != after["url"] {
			t.Fatalf("%v: device changed despite refusal: %+v -> %+v", args, before, after)
		}
	}
}

// --- idempotency ---

func TestConfigDeviceCreateReplaceIsIdempotent(t *testing.T) {
	p := freshConfigPath(t)
	args := []string{"config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--user", "root", "--insecure-tls", "--replace"}
	out1, stderr, code := runConfig(t, p, "", args...)
	if code != 0 {
		t.Fatalf("first: code %d stderr %s", code, stderr)
	}
	if !strings.Contains(out1, "changed") {
		t.Fatalf("first output: %s", out1)
	}
	st1, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	out2, stderr, code := runConfig(t, p, "", append(args, "-o", "json")...)
	if code != 0 {
		t.Fatalf("second: code %d stderr %s", code, stderr)
	}
	if !strings.Contains(out2, `"changed": false`) {
		t.Fatalf("second output should report changed false: %s", out2)
	}
	st2, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatalf("mtime changed: %v -> %v", st1.ModTime(), st2.ModTime())
	}
	if !os.SameFile(st1, st2) {
		t.Fatal("file was rewritten (different inode) despite reporting changed false")
	}
}

func TestConfigDeviceSetIsIdempotent(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net")
	setArgs := []string{"config", "device", "set", "arwen", "--user", "root"}
	if _, stderr, code := runConfig(t, p, "", setArgs...); code != 0 {
		t.Fatalf("first set: code %d stderr %s", code, stderr)
	}
	st1, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runConfig(t, p, "", append(setArgs, "-o", "json")...)
	if code != 0 {
		t.Fatalf("second set: code %d stderr %s", code, stderr)
	}
	if !strings.Contains(out, `"changed": false`) {
		t.Fatalf("second set should be unchanged: %s", out)
	}
	st2, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatalf("mtime changed: %v -> %v", st1.ModTime(), st2.ModTime())
	}
	if !os.SameFile(st1, st2) {
		t.Fatal("file was rewritten (different inode) despite reporting changed false")
	}
}

// --- device set ---

func TestConfigDeviceSetOnlyTouchesGivenFields(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--user", "root")
	_, stderr, code := runConfig(t, p, "", "config", "device", "set", "arwen", "--insecure-tls")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	if dev["url"] != "https://arwen.example.net" || dev["user"] != "root" || dev["insecure_tls"] != true {
		t.Fatalf("device %+v", dev)
	}
}

func TestConfigDeviceSetURLClearsURLFileAndViceVersa(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url-file", "/run/secrets/url")
	if _, stderr, code := runConfig(t, p, "", "config", "device", "set", "arwen", "--url", "https://arwen.example.net"); code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	if dev["url"] != "https://arwen.example.net" {
		t.Fatalf("device %+v", dev)
	}
	if _, ok := dev["url_file"]; ok {
		t.Fatalf("url_file not cleared: %+v", dev)
	}

	if _, stderr, code := runConfig(t, p, "", "config", "device", "set", "arwen", "--url-file", "/run/secrets/url2"); code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	dev = deviceFromConfigFile(t, p, "arwen")
	if _, ok := dev["url"]; ok {
		t.Fatalf("url not cleared: %+v", dev)
	}
	if got, _ := dev["url_file"].(string); !strings.HasSuffix(got, "/run/secrets/url2") {
		t.Fatalf("device %+v", dev)
	}
}

func TestConfigDeviceSetInsecureTLSFalse(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--insecure-tls")
	if _, stderr, code := runConfig(t, p, "", "config", "device", "set", "arwen", "--insecure-tls=false"); code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	if dev["insecure_tls"] != false {
		t.Fatalf("device %+v", dev)
	}
}

func TestConfigDeviceSetUnset(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--user", "root", "--password-file", "/run/secrets/pw")
	if _, stderr, code := runConfig(t, p, "", "config", "device", "set", "arwen", "--unset", "user", "--unset", "password"); code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	if _, ok := dev["user"]; ok {
		t.Fatalf("user not unset: %+v", dev)
	}
	if _, ok := dev["password"]; ok {
		t.Fatalf("password not unset: %+v", dev)
	}
	if _, ok := dev["password_file"]; ok {
		t.Fatalf("password_file not unset: %+v", dev)
	}
}

func TestConfigDeviceSetUnsetURLRequiresReplacement(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net")
	_, stderr, code := runConfig(t, p, "", "config", "device", "set", "arwen", "--unset", "url")
	if code != ExitUsage {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	if dev["url"] != "https://arwen.example.net" {
		t.Fatalf("device changed despite refusal: %+v", dev)
	}

	_, stderr, code = runConfig(t, p, "", "config", "device", "set", "arwen", "--unset", "url", "--url-file", "/run/secrets/url")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	dev = deviceFromConfigFile(t, p, "arwen")
	if _, ok := dev["url"]; ok {
		t.Fatalf("url not cleared: %+v", dev)
	}
}

func TestConfigDeviceSetMissingDeviceRefused(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "other", "--url", "https://other.example.net")
	_, stderr, code := runConfig(t, p, "", "config", "device", "set", "arwen", "--user", "root")
	if code != ExitUsage || !strings.Contains(stderr, "arwen") {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

func TestConfigDeviceSetPreservesUnknownKeys(t *testing.T) {
	p := freshConfigPath(t)
	initial := `{"devices":{"arwen":{"url":"https://arwen.example.net","future_field":"kept"}},"default_device":"arwen","top_level_extra":42}`
	if err := os.WriteFile(p, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runConfig(t, p, "", "config", "device", "set", "arwen", "--user", "root"); code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	m := readConfigFile(t, p)
	if m["top_level_extra"] != float64(42) {
		t.Fatalf("top-level extra dropped: %+v", m)
	}
	dev := deviceFromConfigFile(t, p, "arwen")
	if dev["future_field"] != "kept" {
		t.Fatalf("device extra dropped: %+v", dev)
	}
	if dev["user"] != "root" {
		t.Fatalf("new field not applied: %+v", dev)
	}
}

// --- device remove ---

func TestConfigDeviceRemove(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--default")
	if _, stderr, code := runConfig(t, p, "", "config", "device", "remove", "arwen"); code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	m := readConfigFile(t, p)
	devices := m["devices"].(map[string]any)
	if _, ok := devices["arwen"]; ok {
		t.Fatalf("device not removed: %+v", devices)
	}
	if m["default_device"] != "" {
		t.Fatalf("default_device not cleared: %+v", m["default_device"])
	}
}

func TestConfigDeviceRemoveMissingRefusedUnlessIfExists(t *testing.T) {
	p := freshConfigPath(t)
	_, stderr, code := runConfig(t, p, "", "config", "device", "remove", "arwen")
	if code != ExitUsage {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	_, stderr, code = runConfig(t, p, "", "config", "device", "remove", "arwen", "--if-exists")
	if code != 0 {
		t.Fatalf("--if-exists: code %d stderr %s", code, stderr)
	}
}

// TestConfigDeviceRemoveIfExistsMissingConfigFileIsANoOp pins that "remove
// --if-exists" against a config file that does not exist at all leaves no
// file behind and reports changed false, rather than springing an empty
// config file into existence just because WithLocked ran.
func TestConfigDeviceRemoveIfExistsMissingConfigFileIsANoOp(t *testing.T) {
	p := freshConfigPath(t)
	out, stderr, code := runConfig(t, p, "", "config", "device", "remove", "arwen", "--if-exists", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if !strings.Contains(out, `"changed": false`) {
		t.Fatalf("out %s", out)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("config file should not have been created: stat err=%v", err)
	}
}

// --- device list / devices alias ---

func TestConfigDeviceListMatchesDevicesAlias(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--default")
	runConfig(t, p, "", "config", "device", "create", "other", "--url-file", "/run/secrets/other-url")

	out1, _, code1 := runConfig(t, p, "", "config", "device", "list", "-o", "json")
	out2, _, code2 := runConfig(t, p, "", "devices", "-o", "json")
	if code1 != 0 || code2 != 0 || out1 != out2 {
		t.Fatalf("list %q (%d) != devices %q (%d)", out1, code1, out2, code2)
	}

	tout1, _, _ := runConfig(t, p, "", "config", "device", "list")
	tout2, _, _ := runConfig(t, p, "", "devices")
	if tout1 != tout2 {
		t.Fatalf("text list %q != devices %q", tout1, tout2)
	}
	if !strings.Contains(tout1, "arwen") || !strings.Contains(tout1, "other") {
		t.Fatalf("list missing devices: %s", tout1)
	}
}

func TestConfigDeviceListNeverPrintsPassword(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--password-stdin")
	out, _, _ := runConfig(t, p, "hunter2\n", "config", "device", "list")
	if strings.Contains(out, "hunter2") {
		t.Fatalf("password leaked: %s", out)
	}
}

// --- device show ---

func TestConfigDeviceShow(t *testing.T) {
	p := freshConfigPath(t)
	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runConfig(t, p, "", "config", "device", "create", "arwen",
		"--url", "https://arwen.example.net",
		"--password-file", pwFile,
		"--default")

	out, stderr, code := runConfig(t, p, "", "config", "device", "show", "arwen", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if strings.Contains(out, "hunter2") {
		t.Fatalf("password content leaked: %s", out)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("bad json: %v: %s", err, out)
	}
	res := result["result"].(map[string]any)
	if res["name"] != "arwen" {
		t.Fatalf("result %+v", res)
	}
	urlField := res["url"].(map[string]any)
	if urlField["source"] != "value" || urlField["value"] != "https://arwen.example.net" {
		t.Fatalf("url field %+v", urlField)
	}
	userField := res["user"].(map[string]any)
	if userField["source"] != "default" || userField["value"] != "admin" {
		t.Fatalf("user field %+v", userField)
	}
	pwField := res["password"].(map[string]any)
	if pwField["source"] != "file" || pwField["readable"] != true {
		t.Fatalf("password field %+v", pwField)
	}
	if _, ok := pwField["value"]; ok {
		t.Fatalf("password value present: %+v", pwField)
	}
	if res["default"] != true {
		t.Fatalf("default not reported: %+v", res)
	}
}

func TestConfigDeviceShowUnreadablePasswordFile(t *testing.T) {
	p := freshConfigPath(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net", "--password-file", missing)
	out, _, code := runConfig(t, p, "", "config", "device", "show", "arwen", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d out %s", code, out)
	}
	var result map[string]any
	json.Unmarshal([]byte(out), &result)
	res := result["result"].(map[string]any)
	pwField := res["password"].(map[string]any)
	if pwField["readable"] != false {
		t.Fatalf("password field %+v", pwField)
	}
}

func TestConfigDeviceShowEnvPassword(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net")
	t.Setenv("GLKVM_PASSWORD", "envpw")
	out, _, code := runConfig(t, p, "", "config", "device", "show", "arwen", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d out %s", code, out)
	}
	var result map[string]any
	json.Unmarshal([]byte(out), &result)
	res := result["result"].(map[string]any)
	pwField := res["password"].(map[string]any)
	if pwField["source"] != "env" {
		t.Fatalf("password field %+v", pwField)
	}
}

// --- config default ---

func TestConfigDefault(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net")
	runConfig(t, p, "", "config", "device", "create", "other", "--url", "https://other.example.net")

	out, _, code := runConfig(t, p, "", "config", "default")
	if code != 0 || !strings.Contains(out, "no default") {
		t.Fatalf("code %d out %s", code, out)
	}

	_, stderr, code := runConfig(t, p, "", "config", "default", "other")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	out, _, code = runConfig(t, p, "", "config", "default")
	if code != 0 || strings.TrimSpace(out) != "other" {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestConfigDefaultUnknownDeviceRefused(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net")
	_, stderr, code := runConfig(t, p, "", "config", "default", "nope")
	if code != ExitUsage {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
}

// TestConfigDefaultAlwaysReportsChanged pins that "config default NAME"
// always includes "changed" in its JSON result (no omitempty hiding a
// false), and that its text output says "unchanged" the second time,
// matching create/set.
func TestConfigDefaultAlwaysReportsChanged(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net")

	out, stderr, code := runConfig(t, p, "", "config", "default", "arwen", "-o", "json")
	if code != 0 || !strings.Contains(out, `"changed": true`) {
		t.Fatalf("first: code %d out %s stderr %s", code, out, stderr)
	}

	out2, stderr, code := runConfig(t, p, "", "config", "default", "arwen", "-o", "json")
	if code != 0 || !strings.Contains(out2, `"changed": false`) {
		t.Fatalf("second: code %d out %s stderr %s", code, out2, stderr)
	}

	textOut, stderr, code := runConfig(t, p, "", "config", "default", "arwen")
	if code != 0 || !strings.Contains(textOut, "unchanged") {
		t.Fatalf("text: code %d out %s stderr %s", code, textOut, stderr)
	}
}

// --- error classification ---

// errReader always fails with err, so readPasswordStdin's own read error
// path (as opposed to an empty result) can be exercised directly.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadPasswordStdinReadErrorIsUsage(t *testing.T) {
	_, err := readPasswordStdin(errReader{err: errors.New("boom")})
	var ue UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("want UsageError, got %v (%T)", err, err)
	}
}

// TestAbsPathFailureIsUsage forces filepath.Abs to fail by chdir'ing into a
// directory and then removing it out from under the process, so a
// relative path can no longer be resolved against the working directory.
func TestAbsPathFailureIsUsage(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(gone); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	_, err = absPath("relative-path")
	var ue UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("want UsageError, got %v (%T)", err, err)
	}
}

// --- config device show: insecure_tls source ---

// TestConfigDeviceShowInsecureTLSSource pins that insecure_tls reports a
// source too, like url/user/password: "default" when the key is absent
// from the config file, "value" once it has been set explicitly.
func TestConfigDeviceShowInsecureTLSSource(t *testing.T) {
	p := freshConfigPath(t)
	runConfig(t, p, "", "config", "device", "create", "arwen", "--url", "https://arwen.example.net")

	out, stderr, code := runConfig(t, p, "", "config", "device", "show", "arwen", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("bad json: %v: %s", err, out)
	}
	res := result["result"].(map[string]any)
	tls := res["insecure_tls"].(map[string]any)
	if tls["source"] != "default" || tls["value"] != false {
		t.Fatalf("insecure_tls %+v", tls)
	}

	if _, stderr, code := runConfig(t, p, "", "config", "device", "set", "arwen", "--insecure-tls"); code != 0 {
		t.Fatalf("set: code %d stderr %s", code, stderr)
	}
	out2, stderr, code := runConfig(t, p, "", "config", "device", "show", "arwen", "-o", "json")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	var result2 map[string]any
	if err := json.Unmarshal([]byte(out2), &result2); err != nil {
		t.Fatalf("bad json: %v: %s", err, out2)
	}
	res2 := result2["result"].(map[string]any)
	tls2 := res2["insecure_tls"].(map[string]any)
	if tls2["source"] != "value" || tls2["value"] != true {
		t.Fatalf("insecure_tls %+v", tls2)
	}

	textOut, stderr, code := runConfig(t, p, "", "config", "device", "show", "arwen")
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, stderr)
	}
	if !strings.Contains(textOut, "insecure_tls: value true") {
		t.Fatalf("text output missing insecure_tls source: %s", textOut)
	}
}
