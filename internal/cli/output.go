package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/txomon/glinet-pikvm-cli/internal/config"
)

// Exit codes returned by Execute.
const (
	ExitOK     = 0
	ExitDevice = 1
	ExitUsage  = 2
	ExitConfig = 3
)

// UsageError marks a flag or argument error. It maps to ExitUsage.
type UsageError struct{ Msg string }

func (e UsageError) Error() string { return e.Msg }

func usagef(format string, a ...any) error { return UsageError{Msg: fmt.Sprintf(format, a...)} }

// render writes result to w. In json mode it wraps result in the standard
// {"ok":true,"result":...} envelope; otherwise it calls text, if given.
func render(w io.Writer, format string, result any, text func(io.Writer)) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"ok": true, "result": result})
	}
	if text != nil {
		text(w)
	}
	return nil
}

// classify maps an error to its envelope kind and process exit code.
func classify(err error) (string, int) {
	var ue UsageError
	switch {
	case errors.As(err, &ue):
		return "usage", ExitUsage
	case errors.Is(err, config.ErrConfig):
		return "config", ExitConfig
	default:
		return "device", ExitDevice
	}
}

// renderError writes err to w, as a json envelope or as plain text, and
// returns the process exit code to use.
func renderError(w io.Writer, format string, err error) int {
	kind, code := classify(err)
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"ok": false, "error": map[string]string{"kind": kind, "message": err.Error()}})
		return code
	}
	fmt.Fprintf(w, "glkvm: %s\n", err)
	return code
}
