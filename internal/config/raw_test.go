package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadRawMissingFileIsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	rf, err := LoadRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(rf.Devices) != 0 || rf.DefaultDevice != "" || len(rf.Extra) != 0 {
		t.Fatalf("got %+v", rf)
	}
}

func TestWithLockedCreatesDirAndFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "dir", "config.json")
	changed, err := WithLocked(p, func(rf *RawFile) (*RawFile, error) {
		rf.Devices["a"] = RawDevice{"url": rawString("https://a.example")}
		return rf, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("want changed true")
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
}

func TestWithLockedUnchangedDoesNotRewrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	mutate := func(rf *RawFile) (*RawFile, error) {
		rf.Devices["a"] = RawDevice{"url": rawString("https://a.example")}
		rf.DefaultDevice = "a"
		return rf, nil
	}
	if _, err := WithLocked(p, mutate); err != nil {
		t.Fatal(err)
	}
	st1, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := WithLocked(p, mutate)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("want changed false on second identical write")
	}
	st2, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatalf("mtime changed: %v -> %v", st1.ModTime(), st2.ModTime())
	}
	// mtime alone can't catch a rewrite whose new content happens to land
	// in the same second as the old one; same-inode is the real proof that
	// no rename happened.
	if !os.SameFile(st1, st2) {
		t.Fatal("file was rewritten (different inode) despite reporting changed false")
	}
}

func TestWithLockedPreservesUnknownKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	initial := `{"devices":{"a":{"url":"https://a.example","future_field":"kept"}},"default_device":"a","top_level_extra":42}`
	if err := os.WriteFile(p, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := WithLocked(p, func(rf *RawFile) (*RawFile, error) {
		dev := rf.Devices["a"]
		dev["insecure_tls"] = rawBool(true)
		rf.Devices["a"] = dev
		return rf, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("want changed true")
	}

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["top_level_extra"] != float64(42) {
		t.Fatalf("top-level extra key dropped: %+v", got)
	}
	devices := got["devices"].(map[string]any)
	dev := devices["a"].(map[string]any)
	if dev["future_field"] != "kept" {
		t.Fatalf("device extra key dropped: %+v", dev)
	}
	if dev["insecure_tls"] != true {
		t.Fatalf("new key not applied: %+v", dev)
	}
}

func TestWithLockedPropagatesFnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	wantErr := usageErrorStub{"boom"}
	_, err := WithLocked(p, func(rf *RawFile) (*RawFile, error) {
		return nil, wantErr
	})
	if err != wantErr {
		t.Fatalf("got %v, want %v", err, wantErr)
	}
	if _, statErr := os.Stat(p); statErr == nil {
		t.Fatal("file should not have been created")
	}
}

// usageErrorStub stands in for cli.UsageError without importing the cli
// package (which would create an import cycle); WithLocked must propagate
// fn's error value unchanged regardless of its concrete type.
type usageErrorStub struct{ msg string }

func (e usageErrorStub) Error() string { return e.msg }

func TestConcurrentSetsOnDifferentDevicesNeverLoseAnUpdate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	const names = 2
	const rounds = 20

	var wg sync.WaitGroup
	for i := 0; i < names; i++ {
		name := string(rune('a' + i))
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				url := "https://" + name + "-" + string(rune('0'+r%10)) + ".example"
				_, err := WithLocked(p, func(rf *RawFile) (*RawFile, error) {
					rf.Devices[name] = RawDevice{"url": rawString(url)}
					return rf, nil
				})
				if err != nil {
					t.Errorf("%s round %d: %v", name, r, err)
				}
			}
		}(name)
	}
	wg.Wait()

	rf, err := LoadRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(rf.Devices) != names {
		t.Fatalf("got %d devices, want %d: %+v", len(rf.Devices), names, rf.Devices)
	}
	for i := 0; i < names; i++ {
		name := string(rune('a' + i))
		if _, ok := rf.Devices[name]; !ok {
			t.Fatalf("device %q lost", name)
		}
	}
}

func rawString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func rawBool(v bool) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// TestWithLockedFollowsSymlinkToTarget pins that WithLocked writes through a
// symlinked config path: the symlink itself must survive (never replaced by
// a plain file), and the write must land on its target, so a tool that
// manages config.json as a symlink (e.g. a home-manager activation) is not
// clobbered by glkvm's own atomic rename.
func TestWithLockedFollowsSymlinkToTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real-config.json")
	if err := os.WriteFile(target, []byte(`{"devices":{},"default_device":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}

	changed, err := WithLocked(linkPath, func(rf *RawFile) (*RawFile, error) {
		rf.Devices["a"] = RawDevice{"url": rawString("https://a.example")}
		return rf, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("want changed true")
	}

	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced with a regular file")
	}
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("symlink now points to %q, want %q", got, target)
	}

	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"a"`) {
		t.Fatalf("symlink target was not updated: %s", b)
	}

	lockFi, err := os.Lstat(linkPath + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if lockFi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("lock file should live at the unresolved path, not follow the symlink")
	}
}

// TestWithLockedSymlinkToReadOnlyTargetFailsLoudly pins that a symlink
// pointing into a read-only directory fails the write as an ErrConfig
// (which the cli package's classify maps to exit 3), rather than silently
// discarding the symlink and writing a plain file in its place.
func TestWithLockedSymlinkToReadOnlyTargetFailsLoudly(t *testing.T) {
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

	_, err := WithLocked(linkPath, func(rf *RawFile) (*RawFile, error) {
		rf.Devices["a"] = RawDevice{"url": rawString("https://a.example")}
		return rf, nil
	})
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("want ErrConfig, got %v", err)
	}

	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced despite the failed write")
	}
	got, err := os.Readlink(linkPath)
	if err != nil || got != target {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
}

// TestWithLockedNoOpOnMissingFileDoesNotCreateIt pins the "remove
// --if-exists on a config file that doesn't exist yet" fix: fn returning
// the RawFile unchanged (LoadRaw's own empty result) must compare equal to
// "the file doesn't exist" and skip the write, not silently create an
// empty config file and report changed true.
func TestWithLockedNoOpOnMissingFileDoesNotCreateIt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	changed, err := WithLocked(p, func(rf *RawFile) (*RawFile, error) {
		return rf, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("want changed false")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("config file should not have been created: stat err=%v", err)
	}
}
