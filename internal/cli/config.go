package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/txomon/glinet-pikvm-cli/internal/config"
)

// configFieldNames are the fields "config device set --unset" accepts.
var configFieldNames = []string{"url", "user", "password", "insecure_tls"}

// --- shared helpers ---

// validateDeviceURL requires raw to parse as an http or https URL with a
// host, refusing anything else as a UsageError: a typo'd scheme or a bare
// host with no scheme would otherwise reach the device layer as a much more
// confusing failure later.
func validateDeviceURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return usagef("invalid url %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return usagef("invalid url %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return usagef("invalid url %q: missing host", raw)
	}
	return nil
}

// absPath makes path absolute, for storing a *_file reference. An empty
// path is returned unchanged (nothing was given).
func absPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", usagef("resolve absolute path for %s: %v", path, err)
	}
	return abs, nil
}

// readPasswordStdin reads one line from r, trimming its trailing newline,
// and refuses an empty result: an empty password read from stdin is always
// a mistake (empty input, wrong file redirected, and so on), never an
// intentional way to clear a password (--unset password does that). A read
// failure is a usage error, like every other problem with how
// --password-stdin was invoked (empty input, both --password-file and
// --password-stdin given, and so on).
func readPasswordStdin(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", usagef("read stdin: %v", err)
	}
	line = config.TrimTrailingNewline(line)
	if line == "" {
		return "", usagef("--password-stdin requires a non-empty password")
	}
	return line, nil
}

// requireNonEmptyIfChanged refuses an empty value for a flag that was
// explicitly passed: "--user-file ”" (Changed true, value empty) would
// otherwise silently store an empty path instead of being caught here.
// Flags never given at all are unaffected: their zero value just means
// "nothing given", handled elsewhere.
func requireNonEmptyIfChanged(f *pflag.FlagSet, flag, value, noun string) error {
	if f.Changed(flag) && value == "" {
		return usagef("--%s requires a non-empty %s", flag, noun)
	}
	return nil
}

func rawString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func rawBool(v bool) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// --- config path ---

type configPathResult struct {
	Path string `json:"path"`
}

func newConfigPathCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "path",
		Short:         "Print the resolved config file path",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		result := configPathResult{Path: g.configPath}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintln(w, result.Path)
		})
	}
	return cmd
}

// --- config device (parent) ---

func newConfigDeviceCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "device",
		Short:         "Create, edit, remove, list or inspect configured devices",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newConfigDeviceCreateCmd(g))
	cmd.AddCommand(newConfigDeviceSetCmd(g))
	cmd.AddCommand(newConfigDeviceRemoveCmd(g))
	cmd.AddCommand(newConfigDeviceListCmd(g))
	cmd.AddCommand(newConfigDeviceShowCmd(g))
	return cmd
}

func newConfigCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "config",
		Short:         "Manage the config file: device profiles and file-referenced secrets",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(newConfigPathCmd(g))
	cmd.AddCommand(newConfigDeviceCmd(g))
	cmd.AddCommand(newConfigDefaultCmd(g))
	return cmd
}

// --- config device create ---

// configDeviceResult is the result of "config device create" and "config
// device set".
type configDeviceResult struct {
	Name    string `json:"name"`
	Changed bool   `json:"changed"`
}

func renderConfigDeviceResult(cmd *cobra.Command, g *globals, result configDeviceResult) error {
	return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
		verb := "unchanged"
		if result.Changed {
			verb = "changed"
		}
		fmt.Fprintf(w, "device %q: %s\n", result.Name, verb)
	})
}

func newConfigDeviceCreateCmd(g *globals) *cobra.Command {
	var urlStr, urlFile, user, userFile, passwordFile string
	var passwordStdin, insecureTLS, isDefault, replace bool

	cmd := &cobra.Command{
		Use:           "create NAME",
		Short:         "Create a new device (or replace one with --replace)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return usagef("config device create requires exactly one device name, got %d", len(args))
		}
		return nil
	}
	cmd.Flags().StringVar(&urlStr, "url", "", "device URL (http:// or https://)")
	cmd.Flags().StringVar(&urlFile, "url-file", "", "path to a file holding the device URL, read fresh on every use")
	cmd.Flags().StringVar(&user, "user", "", "username (default: admin)")
	cmd.Flags().StringVar(&userFile, "user-file", "", "path to a file holding the username, read fresh on every use")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "path to a file holding the password, read fresh on every use")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin (one line) and store it inline")
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false, "skip TLS certificate verification")
	cmd.Flags().BoolVar(&isDefault, "default", false, "make this the default device")
	cmd.Flags().BoolVar(&replace, "replace", false, "replace an existing device entirely, dropping its old fields")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		name := args[0]
		f := cmd.Flags()

		if f.Changed("url") == f.Changed("url-file") {
			return usagef("config device create requires exactly one of --url or --url-file")
		}
		if f.Changed("user") && f.Changed("user-file") {
			return usagef("give at most one of --user or --user-file")
		}
		if f.Changed("password-file") && passwordStdin {
			return usagef("give at most one of --password-file or --password-stdin")
		}
		if f.Changed("url") {
			if err := validateDeviceURL(urlStr); err != nil {
				return err
			}
		}
		for _, check := range []struct{ flag, value, noun string }{
			{"url-file", urlFile, "path"},
			{"user", user, "value"},
			{"user-file", userFile, "path"},
			{"password-file", passwordFile, "path"},
		} {
			if err := requireNonEmptyIfChanged(f, check.flag, check.value, check.noun); err != nil {
				return err
			}
		}

		var password string
		if passwordStdin {
			pw, err := readPasswordStdin(cmd.InOrStdin())
			if err != nil {
				return err
			}
			password = pw
		}

		absURLFile, err := absPath(urlFile)
		if err != nil {
			return err
		}
		absUserFile, err := absPath(userFile)
		if err != nil {
			return err
		}
		absPasswordFile, err := absPath(passwordFile)
		if err != nil {
			return err
		}

		changed, err := config.WithLocked(g.configPath, func(rf *config.RawFile) (*config.RawFile, error) {
			if _, exists := rf.Devices[name]; exists && !replace {
				return nil, usagef("device %q already exists, pass --replace to replace it", name)
			}

			dev := config.RawDevice{}
			if f.Changed("url") {
				dev["url"] = rawString(urlStr)
			} else {
				dev["url_file"] = rawString(absURLFile)
			}
			switch {
			case f.Changed("user"):
				dev["user"] = rawString(user)
			case f.Changed("user-file"):
				dev["user_file"] = rawString(absUserFile)
			}
			switch {
			case passwordStdin:
				dev["password"] = rawString(password)
			case f.Changed("password-file"):
				dev["password_file"] = rawString(absPasswordFile)
			}
			if insecureTLS {
				dev["insecure_tls"] = rawBool(true)
			}

			rf.Devices[name] = dev
			if isDefault {
				rf.DefaultDevice = name
			}
			return rf, nil
		})
		if err != nil {
			return err
		}
		return renderConfigDeviceResult(cmd, g, configDeviceResult{Name: name, Changed: changed})
	}
	return cmd
}

// --- config device set ---

func newConfigDeviceSetCmd(g *globals) *cobra.Command {
	var urlStr, urlFile, user, userFile, passwordFile string
	var passwordStdin, insecureTLS, isDefault bool
	var unset []string

	cmd := &cobra.Command{
		Use:           "set NAME",
		Short:         "Change an existing device's fields",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return usagef("config device set requires exactly one device name, got %d", len(args))
		}
		return nil
	}
	cmd.Flags().StringVar(&urlStr, "url", "", "device URL (http:// or https://)")
	cmd.Flags().StringVar(&urlFile, "url-file", "", "path to a file holding the device URL, read fresh on every use")
	cmd.Flags().StringVar(&user, "user", "", "username")
	cmd.Flags().StringVar(&userFile, "user-file", "", "path to a file holding the username, read fresh on every use")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "path to a file holding the password, read fresh on every use")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin (one line) and store it inline")
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false, "skip TLS certificate verification (true/false)")
	cmd.Flags().BoolVar(&isDefault, "default", false, "make this the default device")
	cmd.Flags().StringArrayVar(&unset, "unset", nil, "remove a field and its file counterpart (repeatable): url, user, password, insecure_tls")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		name := args[0]
		f := cmd.Flags()

		if f.Changed("url") && f.Changed("url-file") {
			return usagef("give at most one of --url or --url-file")
		}
		if f.Changed("user") && f.Changed("user-file") {
			return usagef("give at most one of --user or --user-file")
		}
		if f.Changed("password-file") && passwordStdin {
			return usagef("give at most one of --password-file or --password-stdin")
		}
		if f.Changed("url") {
			if err := validateDeviceURL(urlStr); err != nil {
				return err
			}
		}
		for _, check := range []struct{ flag, value, noun string }{
			{"url-file", urlFile, "path"},
			{"user", user, "value"},
			{"user-file", userFile, "path"},
			{"password-file", passwordFile, "path"},
		} {
			if err := requireNonEmptyIfChanged(f, check.flag, check.value, check.noun); err != nil {
				return err
			}
		}

		unsetFields := map[string]bool{}
		for _, name := range unset {
			found := false
			for _, want := range configFieldNames {
				if name == want {
					found = true
					break
				}
			}
			if !found {
				return usagef("--unset: unknown field %q (want url, user, password or insecure_tls)", name)
			}
			unsetFields[name] = true
		}
		if unsetFields["url"] && !f.Changed("url") && !f.Changed("url-file") {
			return usagef("--unset url requires --url or --url-file in the same command")
		}

		var password string
		if passwordStdin {
			pw, err := readPasswordStdin(cmd.InOrStdin())
			if err != nil {
				return err
			}
			password = pw
		}

		absURLFile, err := absPath(urlFile)
		if err != nil {
			return err
		}
		absUserFile, err := absPath(userFile)
		if err != nil {
			return err
		}
		absPasswordFile, err := absPath(passwordFile)
		if err != nil {
			return err
		}

		changed, err := config.WithLocked(g.configPath, func(rf *config.RawFile) (*config.RawFile, error) {
			dev, exists := rf.Devices[name]
			if !exists {
				return nil, usagef("device %q does not exist", name)
			}
			if dev == nil {
				dev = config.RawDevice{}
			}

			for field := range unsetFields {
				switch field {
				case "url":
					delete(dev, "url")
					delete(dev, "url_file")
				case "user":
					delete(dev, "user")
					delete(dev, "user_file")
				case "password":
					delete(dev, "password")
					delete(dev, "password_file")
				case "insecure_tls":
					delete(dev, "insecure_tls")
				}
			}

			switch {
			case f.Changed("url"):
				dev["url"] = rawString(urlStr)
				delete(dev, "url_file")
			case f.Changed("url-file"):
				dev["url_file"] = rawString(absURLFile)
				delete(dev, "url")
			}
			switch {
			case f.Changed("user"):
				dev["user"] = rawString(user)
				delete(dev, "user_file")
			case f.Changed("user-file"):
				dev["user_file"] = rawString(absUserFile)
				delete(dev, "user")
			}
			switch {
			case passwordStdin:
				dev["password"] = rawString(password)
				delete(dev, "password_file")
			case f.Changed("password-file"):
				dev["password_file"] = rawString(absPasswordFile)
				delete(dev, "password")
			}
			if f.Changed("insecure-tls") {
				dev["insecure_tls"] = rawBool(insecureTLS)
			}

			rf.Devices[name] = dev
			if isDefault {
				rf.DefaultDevice = name
			}
			return rf, nil
		})
		if err != nil {
			return err
		}
		return renderConfigDeviceResult(cmd, g, configDeviceResult{Name: name, Changed: changed})
	}
	return cmd
}

// --- config device remove ---

type configDeviceRemoveResult struct {
	Name    string `json:"name"`
	Existed bool   `json:"existed"`
	Changed bool   `json:"changed"`
}

func newConfigDeviceRemoveCmd(g *globals) *cobra.Command {
	var ifExists bool
	cmd := &cobra.Command{
		Use:           "remove NAME",
		Short:         "Delete a configured device",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return usagef("config device remove requires exactly one device name, got %d", len(args))
		}
		return nil
	}
	cmd.Flags().BoolVar(&ifExists, "if-exists", false, "do not fail if the device does not exist")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		name := args[0]
		var existed bool

		changed, err := config.WithLocked(g.configPath, func(rf *config.RawFile) (*config.RawFile, error) {
			_, existed = rf.Devices[name]
			if !existed {
				if ifExists {
					return rf, nil
				}
				return nil, usagef("device %q does not exist", name)
			}
			delete(rf.Devices, name)
			if rf.DefaultDevice == name {
				rf.DefaultDevice = ""
			}
			return rf, nil
		})
		if err != nil {
			return err
		}
		result := configDeviceRemoveResult{Name: name, Existed: existed, Changed: changed}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			if !existed {
				fmt.Fprintf(w, "device %q does not exist\n", name)
				return
			}
			fmt.Fprintf(w, "removed device %q\n", name)
		})
	}
	return cmd
}

// --- config device list / glkvm devices ---

// configDeviceEntry is one configured device in "config device list" and
// "devices"' result. It never includes the password, and shows the
// device's url only when it is given inline: a file-based url shows the
// path, never the resolved value.
type configDeviceEntry struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	URLFile string `json:"url_file,omitempty"`
	Default bool   `json:"default"`
}

func buildDeviceListEntries(f *config.File) []configDeviceEntry {
	names := f.Names()
	entries := make([]configDeviceEntry, 0, len(names))
	for _, name := range names {
		d := f.Devices[name]
		e := configDeviceEntry{Name: name, Default: name == f.DefaultDevice}
		if d.URLFile != "" {
			e.URLFile = d.URLFile
		} else {
			e.URL = d.URL
		}
		entries = append(entries, e)
	}
	return entries
}

// runDeviceList is "config device list"'s RunE, shared verbatim by "devices"
// so the two commands can never drift apart.
func runDeviceList(g *globals, cmd *cobra.Command) error {
	f, err := config.Load(g.configPath)
	if err != nil {
		return err
	}
	if w := config.PermWarning(g.configPath); w != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), w)
	}
	entries := buildDeviceListEntries(f)
	return render(cmd.OutOrStdout(), g.output, entries, func(w io.Writer) {
		for _, e := range entries {
			mark := " "
			if e.Default {
				mark = "*"
			}
			val := e.URL
			if e.URLFile != "" {
				val = "file:" + e.URLFile
			}
			fmt.Fprintf(w, "%s %-16s %s\n", mark, e.Name, val)
		}
	})
}

func newConfigDeviceListCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "list",
		Short:         "List configured devices",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runDeviceList(g, cmd)
	}
	return cmd
}

func newDevicesCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "devices",
		Short:         "List configured devices (alias for \"config device list\")",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runDeviceList(g, cmd)
	}
	return cmd
}

// --- config device show ---

// configFieldStatus is one field's status in "config device show"'s
// result. Source is one of "value", "file", "default", "env" or "unset".
// Value is populated for "value" and "default", never for a secret field
// (password): there it is left empty so the config file's contents never
// appear in the command's own output. File and Readable are populated for
// "file", reporting only whether the referenced file currently reads
// successfully, never what it holds.
type configFieldStatus struct {
	Source   string `json:"source"`
	Value    string `json:"value,omitempty"`
	File     string `json:"file,omitempty"`
	Readable *bool  `json:"readable,omitempty"`
}

// configBoolFieldStatus is insecure_tls's status in "config device show"'s
// result: unlike url/user/password it has no file form, so its only
// possible sources are "value" (the key is present in the config file) and
// "default" (the key is absent, so it resolves to false).
type configBoolFieldStatus struct {
	Source string `json:"source"`
	Value  bool   `json:"value"`
}

func describeBoolField(value, present bool) configBoolFieldStatus {
	source := "default"
	if present {
		source = "value"
	}
	return configBoolFieldStatus{Source: source, Value: value}
}

type configDeviceShowResult struct {
	Name        string                `json:"name"`
	URL         configFieldStatus     `json:"url"`
	User        configFieldStatus     `json:"user"`
	Password    configFieldStatus     `json:"password"`
	InsecureTLS configBoolFieldStatus `json:"insecure_tls"`
	Default     bool                  `json:"default"`
}

// describeField builds one field's show status. secret suppresses Value
// even when the field is set inline, used for password so its content
// never appears in "config device show"'s output.
func describeField(value, file, defaultValue, envValue string, secret bool) configFieldStatus {
	switch {
	case envValue != "":
		return configFieldStatus{Source: "env"}
	case value != "":
		st := configFieldStatus{Source: "value"}
		if !secret {
			st.Value = value
		}
		return st
	case file != "":
		readable := config.CheckFileReadable(file) == nil
		return configFieldStatus{Source: "file", File: file, Readable: &readable}
	case defaultValue != "":
		return configFieldStatus{Source: "default", Value: defaultValue}
	default:
		return configFieldStatus{Source: "unset"}
	}
}

func buildShowResult(d config.Device, defaultDevice string, insecureTLSPresent bool) configDeviceShowResult {
	envPassword := os.Getenv("GLKVM_PASSWORD")
	return configDeviceShowResult{
		Name:        d.Name,
		URL:         describeField(d.URL, d.URLFile, "", "", false),
		User:        describeField(d.User, d.UserFile, "admin", "", false),
		Password:    describeField(d.Password, d.PasswordFile, "", envPassword, true),
		InsecureTLS: describeBoolField(d.InsecureTLS, insecureTLSPresent),
		Default:     d.Name == defaultDevice,
	}
}

func readableWord(r *bool) string {
	if r != nil && *r {
		return "readable"
	}
	return "unreadable"
}

// fieldLine renders a non-secret field's status (url, user) as one line's
// worth of text.
func fieldLine(st configFieldStatus) string {
	switch st.Source {
	case "value":
		return fmt.Sprintf("value %s", st.Value)
	case "file":
		return fmt.Sprintf("file %s (%s)", st.File, readableWord(st.Readable))
	case "default":
		return fmt.Sprintf("default %s", st.Value)
	default:
		return "unset"
	}
}

// passwordLine renders the password field's status: never its value, only
// whether one is set and, for a file, whether it currently reads.
func passwordLine(st configFieldStatus) string {
	switch st.Source {
	case "value":
		return "set (value)"
	case "file":
		return fmt.Sprintf("set (file %s, %s)", st.File, readableWord(st.Readable))
	case "env":
		return "set (env GLKVM_PASSWORD)"
	default:
		return "unset"
	}
}

func newConfigDeviceShowCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "show NAME",
		Short:         "Show a device's fields and where each one comes from",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return usagef("config device show requires exactly one device name, got %d", len(args))
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		// LoadBoth reads the file once and derives both the typed and raw
		// views from that single snapshot, so a write landing between two
		// separate reads can never make show report a mix of before/after
		// state.
		f, rawFile, err := config.LoadBoth(g.configPath)
		if err != nil {
			return err
		}
		d, err := f.Lookup(args[0])
		if err != nil {
			return err
		}
		_, insecureTLSPresent := rawFile.Devices[d.Name]["insecure_tls"]

		result := buildShowResult(d, f.DefaultDevice, insecureTLSPresent)
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintf(w, "name: %s\n", result.Name)
			fmt.Fprintf(w, "url: %s\n", fieldLine(result.URL))
			fmt.Fprintf(w, "user: %s\n", fieldLine(result.User))
			fmt.Fprintf(w, "password: %s\n", passwordLine(result.Password))
			fmt.Fprintf(w, "insecure_tls: %s %v\n", result.InsecureTLS.Source, result.InsecureTLS.Value)
			fmt.Fprintf(w, "default: %v\n", result.Default)
		})
	}
	return cmd
}

// --- config default ---

type configDefaultResult struct {
	Default string `json:"default"`
	Changed bool   `json:"changed"`
}

func newConfigDefaultCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "default [NAME]",
		Short:         "Show or set the default device",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) > 1 {
			return usagef("config default takes at most one argument, got %d", len(args))
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			f, err := config.Load(g.configPath)
			if err != nil {
				return err
			}
			result := configDefaultResult{Default: f.DefaultDevice}
			return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
				if result.Default == "" {
					fmt.Fprintln(w, "no default device set")
					return
				}
				fmt.Fprintln(w, result.Default)
			})
		}

		name := args[0]
		changed, err := config.WithLocked(g.configPath, func(rf *config.RawFile) (*config.RawFile, error) {
			if _, ok := rf.Devices[name]; !ok {
				return nil, usagef("device %q does not exist", name)
			}
			rf.DefaultDevice = name
			return rf, nil
		})
		if err != nil {
			return err
		}
		result := configDefaultResult{Default: name, Changed: changed}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			verb := "unchanged"
			if result.Changed {
				verb = "changed"
			}
			fmt.Fprintf(w, "default device %q: %s\n", name, verb)
		})
	}
	return cmd
}
