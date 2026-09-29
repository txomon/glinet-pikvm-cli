package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// RawDevice is one device's fields as raw JSON, preserving keys a config
// command does not understand instead of dropping them on a rewrite.
type RawDevice map[string]json.RawMessage

// RawFile is the whole config file as raw JSON. Devices and DefaultDevice
// are decoded views over the "devices" and "default_device" top-level keys;
// Extra holds every other top-level key exactly as read, untouched by any
// command that does not mean to change it.
type RawFile struct {
	Devices       map[string]RawDevice
	DefaultDevice string
	Extra         map[string]json.RawMessage
}

// LoadRaw reads path as a RawFile. A missing file is not an error: it
// returns an empty RawFile, so a config command can create the file and
// its directory on first use.
func LoadRaw(path string) (*RawFile, error) {
	rf := &RawFile{Devices: map[string]RawDevice{}, Extra: map[string]json.RawMessage{}}

	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return rf, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrConfig, path, err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", ErrConfig, path, err)
	}
	for k, v := range top {
		switch k {
		case "devices":
			var devs map[string]RawDevice
			if err := json.Unmarshal(v, &devs); err != nil {
				return nil, fmt.Errorf("%w: parse %s: devices: %v", ErrConfig, path, err)
			}
			rf.Devices = devs
			if rf.Devices == nil {
				rf.Devices = map[string]RawDevice{}
			}
		case "default_device":
			if err := json.Unmarshal(v, &rf.DefaultDevice); err != nil {
				return nil, fmt.Errorf("%w: parse %s: default_device: %v", ErrConfig, path, err)
			}
		default:
			rf.Extra[k] = v
		}
	}
	return rf, nil
}

// marshal renders rf as two-space-indented JSON with a trailing newline.
// Every map involved (top, devices, and each device's own fields) sorts its
// keys automatically as part of encoding/json's normal map handling, giving
// the stable, sorted-keys output the config file format requires.
func (rf *RawFile) marshal() ([]byte, error) {
	top := make(map[string]any, len(rf.Extra)+2)
	for k, v := range rf.Extra {
		top[k] = v
	}
	top["devices"] = rf.Devices
	top["default_device"] = rf.DefaultDevice

	b, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// jsonSemanticallyEqual reports whether a and b encode the same JSON value,
// ignoring key order and whitespace, by round-tripping both through a
// generic decode and a canonical re-encode.
func jsonSemanticallyEqual(a, b []byte) (bool, error) {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false, err
	}
	ab, err := json.Marshal(av)
	if err != nil {
		return false, err
	}
	bb, err := json.Marshal(bv)
	if err != nil {
		return false, err
	}
	return string(ab) == string(bb), nil
}

// atomicWrite writes data to path by writing a temp file in the same
// directory, fsyncing it, chmod'ing it 0600, then renaming it over path.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: create temp file in %s: %v", ErrConfig, dir, err)
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: write %s: %v", ErrConfig, tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: fsync %s: %v", ErrConfig, tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: close %s: %v", ErrConfig, tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("%w: chmod %s: %v", ErrConfig, tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("%w: rename %s to %s: %v", ErrConfig, tmpPath, path, err)
	}
	ok = true
	return nil
}

// WithLocked runs fn with the config file at path locked against concurrent
// writers (a provisioning run and an interactive user command, for
// example): it creates path's directory (0700) if missing, takes an
// exclusive flock on path+".lock" for the duration of the call, loads the
// current RawFile fresh from disk, and passes it to fn. fn returns the
// RawFile to write back (typically the same value, mutated) or an error,
// which WithLocked propagates unchanged so the caller's own error
// classification (usage, config, ...) still applies. The file is rewritten
// only when fn's result is not semantically identical (same decoded JSON,
// ignoring key order and whitespace) to what is already on disk; changed
// reports whether a write happened.
func WithLocked(path string, fn func(*RawFile) (*RawFile, error)) (changed bool, err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("%w: mkdir %s: %v", ErrConfig, dir, err)
	}

	lockPath := path + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("%w: open lock file %s: %v", ErrConfig, lockPath, err)
	}
	defer lf.Close()
	if err := unix.Flock(int(lf.Fd()), unix.LOCK_EX); err != nil {
		return false, fmt.Errorf("%w: lock %s: %v", ErrConfig, lockPath, err)
	}
	defer unix.Flock(int(lf.Fd()), unix.LOCK_UN)

	rf, err := LoadRaw(path)
	if err != nil {
		return false, err
	}

	newRF, err := fn(rf)
	if err != nil {
		return false, err
	}

	newBytes, err := newRF.marshal()
	if err != nil {
		return false, fmt.Errorf("%w: encode %s: %v", ErrConfig, path, err)
	}

	if oldBytes, statErr := os.ReadFile(path); statErr == nil {
		eq, eqErr := jsonSemanticallyEqual(oldBytes, newBytes)
		if eqErr != nil {
			return false, fmt.Errorf("%w: compare %s: %v", ErrConfig, path, eqErr)
		}
		if eq {
			return false, nil
		}
	}

	if err := atomicWrite(path, newBytes); err != nil {
		return false, err
	}
	return true, nil
}
