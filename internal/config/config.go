// Package config loads glkvm's device configuration file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrConfig is wrapped by every error this package returns.
var ErrConfig = errors.New("config")

// Device holds the connection details for one configured KVM. Each of URL,
// User and Password may instead be given as a *File field naming a path to
// read the value from at resolve time; Load rejects a device that sets both
// a value and its file counterpart for the same field. URLFile, UserFile
// and PasswordFile are never resolved by Load itself, only by Device(),
// which re-reads them on every call: Lookup returns the unresolved fields
// as stored, for callers (config commands, doctor) that need to inspect or
// edit them without touching the filesystem.
type Device struct {
	Name         string `json:"-"`
	URL          string `json:"url"`
	URLFile      string `json:"url_file"`
	User         string `json:"user"`
	UserFile     string `json:"user_file"`
	Password     string `json:"password"`
	PasswordFile string `json:"password_file"`
	InsecureTLS  bool   `json:"insecure_tls"`
}

// File is the parsed contents of a config file.
type File struct {
	Devices       map[string]Device `json:"devices"`
	DefaultDevice string            `json:"default_device"`
}

// DefaultPath returns the config path to use when none is given on the
// command line: GLKVM_CONFIG if set, else $HOME/.config/glkvm/config.json.
func DefaultPath() string {
	if p := os.Getenv("GLKVM_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".config/glkvm/config.json"
	}
	return filepath.Join(home, ".config", "glkvm", "config.json")
}

// Load reads and parses the config file at path. It rejects a device that
// sets both a value and its file counterpart for the same field (url,
// user or password), naming the device and field in the error.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrConfig, path, err)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", ErrConfig, path, err)
	}
	for name, d := range f.Devices {
		if d.URL != "" && d.URLFile != "" {
			return nil, fmt.Errorf("%w: device %q: both url and url_file set", ErrConfig, name)
		}
		if d.User != "" && d.UserFile != "" {
			return nil, fmt.Errorf("%w: device %q: both user and user_file set", ErrConfig, name)
		}
		if d.Password != "" && d.PasswordFile != "" {
			return nil, fmt.Errorf("%w: device %q: both password and password_file set", ErrConfig, name)
		}
	}
	return &f, nil
}

// Names returns the configured device names, sorted.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Devices))
	for n := range f.Devices {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Lookup returns the named device's raw, unresolved fields: url_file,
// user_file and password_file are returned exactly as configured, without
// reading them. An empty name means default_device. Callers that need the
// device ready to use (url/user/password filled in) want Device, not
// Lookup; this is for callers that only need to inspect or edit the
// device's configured shape, such as the config commands and doctor's
// per-file readability report.
func (f *File) Lookup(name string) (Device, error) {
	if name == "" {
		name = f.DefaultDevice
	}
	if name == "" {
		return Device{}, fmt.Errorf("%w: no device given and no default_device set", ErrConfig)
	}
	d, ok := f.Devices[name]
	if !ok {
		return Device{}, fmt.Errorf("%w: unknown device %q (configured: %v)", ErrConfig, name, f.Names())
	}
	d.Name = name
	return d, nil
}

// Device looks up a device by name (see Lookup) and resolves it: a
// url_file/user_file/password_file is read fresh from disk on every call,
// with one trailing "\n" or "\r\n" trimmed; a missing or unreadable file is
// a config error naming the device, field and path. user defaults to
// "admin" when neither user nor user_file is set. GLKVM_PASSWORD, if set,
// overrides the resolved password.
func (f *File) Device(name string) (Device, error) {
	d, err := f.Lookup(name)
	if err != nil {
		return Device{}, err
	}

	switch {
	case d.URL != "":
	case d.URLFile != "":
		v, err := readFileValue(d.Name, "url", d.URLFile)
		if err != nil {
			return Device{}, err
		}
		d.URL = v
	}
	if d.URL == "" {
		return Device{}, fmt.Errorf("%w: device %q has no url", ErrConfig, d.Name)
	}

	switch {
	case d.User != "":
	case d.UserFile != "":
		v, err := readFileValue(d.Name, "user", d.UserFile)
		if err != nil {
			return Device{}, err
		}
		d.User = v
	default:
		d.User = "admin"
	}

	switch {
	case d.Password != "":
	case d.PasswordFile != "":
		v, err := readFileValue(d.Name, "password", d.PasswordFile)
		if err != nil {
			return Device{}, err
		}
		d.Password = v
	}

	if p := os.Getenv("GLKVM_PASSWORD"); p != "" {
		d.Password = p
	}
	return d, nil
}

// readFileValue reads the file at path for device name's field, trimming
// one trailing "\n" or "\r\n". A missing or unreadable file is a config
// error naming the device, field and path.
func readFileValue(name, field, path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%w: device %q: %s: read %s: %v", ErrConfig, name, field, path, err)
	}
	return TrimTrailingNewline(string(b)), nil
}

// TrimTrailingNewline strips one trailing "\n" or "\r\n" from s, if present.
func TrimTrailingNewline(s string) string {
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	return s
}

// CheckFileReadable reports whether path can currently be read in full,
// without exposing its contents to the caller: it uses the exact reader
// Device() resolution uses, discarding the value. Used by "config device
// show" and doctor to report a file reference's readability without
// printing what it holds.
func CheckFileReadable(path string) error {
	_, err := os.ReadFile(path)
	return err
}

// PermWarning returns a non-empty warning when path is group or world
// accessible, since the file holds a plaintext password.
func PermWarning(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Sprintf("warning: %s is mode %o and holds a password; chmod 600 it", path, st.Mode().Perm())
	}
	return ""
}
