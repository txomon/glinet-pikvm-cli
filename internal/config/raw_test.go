package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
