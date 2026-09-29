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

// batchError carries a structured result alongside the error that stopped a
// batch, so renderError can print that result instead of the standard
// {"ok":false,"error":...} envelope. Used by "run" so a failed batch's
// receipts are still printed. Its wrapped err is always a plain error, never
// a UsageError, even when the underlying failure (like an out-of-bounds
// coordinate) would otherwise classify as one: an error found while
// executing an already-validated batch is a device-class failure, like any
// other.
type batchError struct {
	result any
	err    error
}

func (e *batchError) Error() string { return e.err.Error() }
func (e *batchError) Unwrap() error { return e.err }

// renderError writes err to w, as a json envelope or as plain text, and
// returns the process exit code to use.
func renderError(w io.Writer, format string, err error) int {
	var be *batchError
	if errors.As(err, &be) {
		_, code := classify(be.err)
		if format == "json" {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			_ = enc.Encode(be.result)
			return code
		}
		fmt.Fprintf(w, "glkvm: %s\n", be.err)
		return code
	}

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
