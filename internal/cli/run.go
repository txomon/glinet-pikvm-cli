package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/txomon/glinet-pikvm-cli/internal/keys"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
)

// maxBatchActions and maxWaitMs bound a "run" action batch. Both are checked
// during validation, before any device call.
const (
	maxBatchActions = 100
	maxWaitMs       = 60000
)

// rawAction is one action in a "run" batch, as parsed from JSON. Pointer
// fields distinguish "absent" from the type's zero value, which matters for
// validation: (0,0) is a valid pixel coordinate, but a missing "ms" is not a
// valid wait.
type rawAction struct {
	Type   string  `json:"type"`
	Keys   *string `json:"keys"`
	Text   *string `json:"text"`
	Slow   bool    `json:"slow"`
	X      *int    `json:"x"`
	Y      *int    `json:"y"`
	Button *string `json:"button"`
	Dx     *int    `json:"dx"`
	Dy     *int    `json:"dy"`
	Ms     *int    `json:"ms"`
	Port   *int    `json:"port"`
	File   *string `json:"file"`
}

// receipt records the outcome of one attempted action.
type receipt struct {
	Index int    `json:"index"`
	Type  string `json:"type"`
	OK    bool   `json:"ok"`
	At    string `json:"at"`
	Error string `json:"error,omitempty"`
}

// runResult is the result of "run": how many of the batch's actions
// completed, a receipt for each one attempted, and the screenshot taken by
// --observe-after, if any.
type runResult struct {
	Completed  int               `json:"completed"`
	Total      int               `json:"total"`
	Receipts   []receipt         `json:"receipts"`
	Error      string            `json:"error,omitempty"`
	Screenshot *screenshotResult `json:"screenshot,omitempty"`
}

// loadActionsInput returns the raw batch JSON from --actions-json or
// --actions-file, as a UsageError when neither or both are given.
func loadActionsInput(actionsJSON, actionsFile string) ([]byte, error) {
	switch {
	case actionsJSON != "" && actionsFile != "":
		return nil, usagef("--actions-json and --actions-file are mutually exclusive")
	case actionsJSON != "":
		return []byte(actionsJSON), nil
	case actionsFile != "":
		b, err := os.ReadFile(actionsFile)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", actionsFile, err)
		}
		return b, nil
	default:
		return nil, usagef("run requires --actions-json or --actions-file")
	}
}

// parseActions unmarshals raw into a batch of actions, as a UsageError on
// malformed JSON.
func parseActions(raw []byte) ([]rawAction, error) {
	var actions []rawAction
	if err := json.Unmarshal(raw, &actions); err != nil {
		return nil, usagef("invalid action batch: %v", err)
	}
	return actions, nil
}

// intOr dereferences p, or returns def when p is nil.
func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// validateActions checks every action in the batch before any device call:
// batch size, known types, required fields, ranges, and key combo syntax.
// Coordinate bounds depend on the capture size, resolved lazily during
// execution, so they are checked there instead. A failure names the 0-based
// index of the bad action.
func validateActions(actions []rawAction) error {
	if len(actions) > maxBatchActions {
		return usagef("batch has %d actions, at most %d allowed", len(actions), maxBatchActions)
	}
	for i, a := range actions {
		if err := validateAction(a); err != nil {
			return usagef("action %d: %v", i, err)
		}
	}
	return nil
}

// validateAction validates one action against its type's rules. Errors here
// are wrapped with the action's index by validateActions.
func validateAction(a rawAction) error {
	switch a.Type {
	case "key":
		if a.Keys == nil || *a.Keys == "" {
			return fmt.Errorf(`"key" requires "keys"`)
		}
		if _, err := keys.ParseCombo(*a.Keys); err != nil {
			return err
		}
	case "type":
		if a.Text == nil {
			return fmt.Errorf(`"type" requires "text"`)
		}
	case "move":
		if a.X == nil || a.Y == nil {
			return fmt.Errorf(`"move" requires "x" and "y"`)
		}
	case "click":
		if a.X == nil || a.Y == nil {
			return fmt.Errorf(`"click" requires "x" and "y"`)
		}
		if err := validButton(buttonOr(a.Button)); err != nil {
			return err
		}
	case "double_click":
		if a.X == nil || a.Y == nil {
			return fmt.Errorf(`"double_click" requires "x" and "y"`)
		}
	case "scroll":
		if err := validDelta("dx", intOr(a.Dx, 0)); err != nil {
			return err
		}
		if err := validDelta("dy", intOr(a.Dy, 0)); err != nil {
			return err
		}
	case "nudge":
		dx, dy := intOr(a.Dx, 0), intOr(a.Dy, 0)
		if dx == 0 && dy == 0 {
			return fmt.Errorf(`"nudge" requires "dx" or "dy" to be non-zero`)
		}
		if err := validDelta("dx", dx); err != nil {
			return err
		}
		if err := validDelta("dy", dy); err != nil {
			return err
		}
	case "wait":
		if a.Ms == nil {
			return fmt.Errorf(`"wait" requires "ms"`)
		}
		if *a.Ms < 0 || *a.Ms > maxWaitMs {
			return fmt.Errorf("ms must be between 0 and %d, got %d", maxWaitMs, *a.Ms)
		}
	case "port":
		if a.Port == nil {
			return fmt.Errorf(`"port" requires "port"`)
		}
		if *a.Port < 1 || *a.Port > 4 {
			return fmt.Errorf("port must be 1 to 4, got %d", *a.Port)
		}
	case "screenshot":
		if a.File == nil || *a.File == "" {
			return fmt.Errorf(`"screenshot" requires "file"`)
		}
	default:
		return fmt.Errorf("unknown action type %q", a.Type)
	}
	return nil
}

// buttonOr returns *button, or "left" when button is nil.
func buttonOr(button *string) string {
	if button == nil {
		return "left"
	}
	return *button
}

// batchState threads the mutable state through a batch's execution: the
// client, and the capture size resolved lazily for pointer actions.
type batchState struct {
	ctx       context.Context
	c         *kvmd.Client
	w, h      int
	sizeKnown bool
}

// pointerSize resolves and caches the capture size on first use by a pointer
// action. A "port" action invalidates the cache, since a different host may
// report a different resolution.
func (s *batchState) pointerSize() (int, int, error) {
	if s.sizeKnown {
		return s.w, s.h, nil
	}
	w, h, err := resolveSize(s.ctx, s.c, "")
	if err != nil {
		return 0, 0, err
	}
	s.w, s.h, s.sizeKnown = w, h, true
	return w, h, nil
}

// execute runs one already-validated action, reusing the do* functions built
// for the cobra subcommands.
func (s *batchState) execute(a rawAction) error {
	switch a.Type {
	case "key":
		return doKey(s.ctx, s.c, []string{*a.Keys}, 0)
	case "type":
		return doType(s.ctx, s.c, *a.Text, a.Slow, "en-us")
	case "move":
		w, h, err := s.pointerSize()
		if err != nil {
			return err
		}
		return doMouseMove(s.ctx, s.c, *a.X, *a.Y, w, h)
	case "click":
		w, h, err := s.pointerSize()
		if err != nil {
			return err
		}
		return doMouseClick(s.ctx, s.c, *a.X, *a.Y, w, h, buttonOr(a.Button))
	case "double_click":
		w, h, err := s.pointerSize()
		if err != nil {
			return err
		}
		return doMouseDoubleClick(s.ctx, s.c, *a.X, *a.Y, w, h)
	case "scroll":
		return doMouseScroll(s.ctx, s.c, intOr(a.Dx, 0), intOr(a.Dy, 0))
	case "nudge":
		return doMouseNudge(s.ctx, s.c, intOr(a.Dx, 0), intOr(a.Dy, 0))
	case "wait":
		return waitMs(s.ctx, *a.Ms)
	case "port":
		if _, err := doPort(s.ctx, s.c, strconv.Itoa(*a.Port), defaultSettle); err != nil {
			return err
		}
		s.sizeKnown = false
		return nil
	case "screenshot":
		_, data, err := doScreenshot(s.ctx, s.c, *a.File, false, 0)
		if err != nil {
			return err
		}
		return os.WriteFile(*a.File, data, 0o644)
	default:
		return fmt.Errorf("unknown action type %q", a.Type)
	}
}

// waitMs sleeps for ms milliseconds, or returns early if ctx is done.
func waitMs(ctx context.Context, ms int) error {
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// executeBatch runs actions in order, stopping at the first error. Every
// attempted action gets a receipt, whether it succeeded or failed.
func executeBatch(ctx context.Context, c *kvmd.Client, actions []rawAction) runResult {
	result := runResult{Total: len(actions), Receipts: make([]receipt, 0, len(actions))}
	state := &batchState{ctx: ctx, c: c}
	for i, a := range actions {
		err := state.execute(a)
		r := receipt{
			Index: i,
			Type:  a.Type,
			OK:    err == nil,
			At:    time.Now().UTC().Format(time.RFC3339Nano),
		}
		if err != nil {
			r.Error = err.Error()
			result.Receipts = append(result.Receipts, r)
			result.Error = fmt.Sprintf("action %d (%s): %v", i, a.Type, err)
			return result
		}
		result.Receipts = append(result.Receipts, r)
		result.Completed++
	}
	return result
}

// renderRunResult writes a run's result. Unlike other commands it is never
// wrapped in the {"ok":...} envelope: a failed batch renders the same shape
// through renderError's batchError handling, so receipts round-trip
// unchanged whether the batch succeeded or not.
func renderRunResult(w io.Writer, format string, result runResult) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Fprintf(w, "completed %d/%d actions\n", result.Completed, result.Total)
	if result.Screenshot != nil {
		s := result.Screenshot
		fmt.Fprintf(w, "wrote %s (%dx%d %s, %d bytes)\n", s.File, s.Width, s.Height, s.Format, s.Bytes)
	}
	return nil
}

func newRunCmd(g *globals) *cobra.Command {
	var actionsJSON, actionsFile, file string
	var observeAfter bool
	cmd := &cobra.Command{
		Use:           "run",
		Short:         "Run a validated batch of JSON actions, with a receipt per action",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVar(&actionsJSON, "actions-json", "", "action batch as a JSON array")
	cmd.Flags().StringVar(&actionsFile, "actions-file", "", "path to a file holding the action batch as a JSON array")
	cmd.Flags().BoolVar(&observeAfter, "observe-after", false, "take a screenshot after the last action attempted")
	cmd.Flags().StringVar(&file, "file", "", "screenshot output path for --observe-after")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if observeAfter && file == "" {
			return usagef("--observe-after requires --file")
		}
		raw, err := loadActionsInput(actionsJSON, actionsFile)
		if err != nil {
			return err
		}
		actions, err := parseActions(raw)
		if err != nil {
			return err
		}
		if err := validateActions(actions); err != nil {
			return err
		}

		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}

		result := executeBatch(cmd.Context(), c, actions)

		if observeAfter {
			shot, data, shotErr := doScreenshot(cmd.Context(), c, file, false, 0)
			if shotErr == nil {
				if werr := os.WriteFile(file, data, 0o644); werr != nil {
					shotErr = fmt.Errorf("write screenshot to %s: %w", file, werr)
				}
			}
			if shotErr != nil {
				if result.Error == "" {
					result.Error = shotErr.Error()
				}
			} else {
				result.Screenshot = &shot
			}
		}

		if result.Error != "" {
			return &batchError{result: result, err: errors.New(result.Error)}
		}
		return renderRunResult(cmd.OutOrStdout(), g.output, result)
	}
	return cmd
}
