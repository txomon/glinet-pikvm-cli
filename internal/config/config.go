// Package config loads glkvm's device configuration file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ErrConfig is wrapped by every error this package returns.
var ErrConfig = errors.New("config")

// Device holds the connection details for one configured KVM.
type Device struct {
	Name        string `json:"-"`
	URL         string `json:"url"`
	User        string `json:"user"`
	Password    string `json:"password"`
	InsecureTLS bool   `json:"insecure_tls"`
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

// Load reads and parses the config file at path.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrConfig, path, err)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", ErrConfig, path, err)
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

// Device looks up a device by name. An empty name means default_device.
// GLKVM_PASSWORD, if set, overrides the configured password.
func (f *File) Device(name string) (Device, error) {
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
	if d.URL == "" {
		return Device{}, fmt.Errorf("%w: device %q has no url", ErrConfig, name)
	}
	d.Name = name
	if p := os.Getenv("GLKVM_PASSWORD"); p != "" {
		d.Password = p
	}
	return d, nil
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
