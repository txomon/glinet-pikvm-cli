package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/txomon/glinet-pikvm-cli/internal/edid"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
)

// edidReadbackTimeout bounds how long "edid set" waits for a flashed EDID to
// read back correctly from the device.
const edidReadbackTimeout = 10 * time.Second

// edidDefaultSettle is how long "edid set" waits, after a flash, for the
// capture to settle.
const edidDefaultSettle = 15 * time.Second

// edidShowResult is the result of glkvm edid (no subcommand).
type edidShowResult struct {
	Mode     string            `json:"mode"`
	Profile  string            `json:"profile"`
	Capture  string            `json:"capture"`
	Profiles []edidProfileMode `json:"profiles"`
}

// edidProfileMode is one named profile's decoded mode, listed by glkvm edid.
type edidProfileMode struct {
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// edidSetResult is the result of glkvm edid set.
type edidSetResult struct {
	Profile string `json:"profile"`
	Changed bool   `json:"changed"`
	Mode    string `json:"mode"`
	Capture string `json:"capture"`
}

// edidValidateResult is the result of glkvm edid validate.
type edidValidateResult struct {
	Mode    string `json:"mode"`
	Capture string `json:"capture"`
}

// edidDumpResult is the result of glkvm edid dump.
type edidDumpResult struct {
	EDID string `json:"edid"`
	File string `json:"file,omitempty"`
}

// resolveEdidSetHex resolves the hex to flash: from file (read and
// normalized) when file is non-empty, otherwise from profile, either an
// embedded blob (chromebook) or a device-side preset's content, matched
// against presets by the key edid.PresetKey names.
func resolveEdidSetHex(profile, file string, presets []kvmd.EDIDPreset) (string, error) {
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read edid file %s: %w", file, err)
		}
		norm, err := edid.Normalize(string(raw))
		if err != nil {
			return "", usagef("%s: %v", file, err)
		}
		return norm, nil
	}

	key, embedded, err := edid.PresetKey(profile)
	if err != nil {
		return "", UsageError{Msg: err.Error()}
	}
	if embedded != "" {
		return embedded, nil
	}
	for _, p := range presets {
		if p.Key == key {
			return strings.ToLower(p.Content), nil
		}
	}
	return "", fmt.Errorf("device has no edid preset %q for profile %q", key, profile)
}

// captureVerdict reports "ok" when m passes edid.CheckCapture, otherwise the
// failure text.
func captureVerdict(m edid.Mode) string {
	if err := edid.CheckCapture(m); err != nil {
		return err.Error()
	}
	return "ok"
}

// doEdidShow gathers the flashed EDID's mode and matching profile, its
// capture verdict, and every named profile's mode. It is a plain function,
// not a cobra RunE, so other callers can use it without cobra.
func doEdidShow(ctx context.Context, c *kvmd.Client) (edidShowResult, error) {
	flashed, err := c.GetEDID(ctx)
	if err != nil {
		return edidShowResult{}, err
	}
	presets, err := c.EDIDList(ctx)
	if err != nil {
		return edidShowResult{}, err
	}

	mode, err := edid.Parse(flashed)
	if err != nil {
		return edidShowResult{}, fmt.Errorf("parse flashed edid: %w", err)
	}

	result := edidShowResult{
		Mode:    mode.String(),
		Profile: matchEDIDProfile(flashed, presets),
		Capture: captureVerdict(mode),
	}

	for _, name := range edid.Profiles {
		profileHex, err := resolveEdidSetHex(name, "", presets)
		if err != nil {
			return edidShowResult{}, fmt.Errorf("resolve profile %q: %w", name, err)
		}
		m, err := edid.Parse(profileHex)
		if err != nil {
			return edidShowResult{}, fmt.Errorf("parse profile %q: %w", name, err)
		}
		result.Profiles = append(result.Profiles, edidProfileMode{Name: name, Mode: m.String()})
	}

	return result, nil
}

// doEdidSet resolves the hex to flash (profile or --file), rejects a mode
// that fails the capture check unless force is set, skips the flash when the
// device already has identical hex, otherwise flashes and waits for the
// device to read back the new hex, then waits up to settle for the capture
// to become valid. It is a plain function, not a cobra RunE, so other
// callers can use it without cobra.
func doEdidSet(ctx context.Context, c *kvmd.Client, profile, file string, force bool, settle time.Duration) (edidSetResult, error) {
	presets, err := c.EDIDList(ctx)
	if err != nil {
		return edidSetResult{}, err
	}

	newHex, err := resolveEdidSetHex(profile, file, presets)
	if err != nil {
		return edidSetResult{}, err
	}

	mode, err := edid.Parse(newHex)
	if err != nil {
		return edidSetResult{}, fmt.Errorf("parse resolved edid: %w", err)
	}

	if cerr := edid.CheckCapture(mode); cerr != nil && !force {
		return edidSetResult{}, usagef("%s: %v", mode, cerr)
	}

	current, err := c.GetEDID(ctx)
	if err != nil {
		return edidSetResult{}, err
	}

	changed := current != newHex
	if changed {
		if err := c.FlashEDID(ctx, newHex); err != nil {
			return edidSetResult{}, err
		}

		readCtx, cancel := context.WithTimeout(ctx, edidReadbackTimeout)
		defer cancel()
		if err := waitFor(readCtx, waitPollInterval, func() (bool, error) {
			got, err := c.GetEDID(readCtx)
			if err != nil {
				return false, err
			}
			return got == newHex, nil
		}); err != nil {
			return edidSetResult{}, fmt.Errorf("flashed edid did not read back within %s: %w", edidReadbackTimeout, err)
		}
	}

	result := edidSetResult{
		Profile: matchEDIDProfile(newHex, presets),
		Changed: changed,
		Mode:    mode.String(),
	}

	// Wait for the capture to both become valid and land on the flashed
	// mode's own resolution: a real device passes through several
	// intermediate states (hdmi signal down, real_resolution briefly
	// "no_signal", real_resolution ahead of the reported resolution) that
	// StreamerState.Valid alone rejects, but it can also settle on a valid
	// capture that never adopted the flashed mode at all (the downstream
	// source declined to renegotiate). Either way this waits out the full
	// settle duration before giving up; it never returns early on a valid
	// capture at the wrong resolution.
	settleCtx, cancel := context.WithTimeout(ctx, settle)
	defer cancel()
	var last kvmd.StreamerState
	waitErr := waitFor(settleCtx, waitPollInterval, func() (bool, error) {
		st, err := c.Streamer(settleCtx)
		if err != nil {
			return false, err
		}
		last = st
		if st.Valid() != nil {
			return false, nil
		}
		return st.Width == mode.Width && st.Height == mode.Height, nil
	})
	if waitErr != nil && !errors.Is(waitErr, context.DeadlineExceeded) {
		return edidSetResult{}, waitErr
	}

	switch {
	case last.Valid() != nil:
		result.Capture = "no signal"
	case last.Width == mode.Width && last.Height == mode.Height:
		result.Capture = last.RealResolution
	default:
		result.Capture = fmt.Sprintf("%s (does not match flashed mode %s)", last.RealResolution, mode)
	}

	return result, nil
}

// doEdidValidate reads and normalizes an EDID hex file, offline: it never
// contacts the device. It reports the decoded mode and the capture verdict,
// as a UsageError when the mode fails the capture check.
func doEdidValidate(filePath string) (edidValidateResult, error) {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return edidValidateResult{}, fmt.Errorf("read edid file %s: %w", filePath, err)
	}
	norm, err := edid.Normalize(string(raw))
	if err != nil {
		return edidValidateResult{}, usagef("%s: %v", filePath, err)
	}
	mode, err := edid.Parse(norm)
	if err != nil {
		return edidValidateResult{}, fmt.Errorf("parse %s: %w", filePath, err)
	}
	if cerr := edid.CheckCapture(mode); cerr != nil {
		return edidValidateResult{}, usagef("%s: %v", mode, cerr)
	}
	return edidValidateResult{Mode: mode.String(), Capture: "ok"}, nil
}

func newEdidCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "edid",
		Short:         "Show, set, validate or dump the KVM's flashed EDID",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		result, err := doEdidShow(cmd.Context(), c)
		if err != nil {
			return err
		}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintf(w, "flashed: %s (profile=%s)\n", result.Mode, result.Profile)
			fmt.Fprintf(w, "capture: %s\n", result.Capture)
			fmt.Fprintln(w, "profiles:")
			for _, p := range result.Profiles {
				fmt.Fprintf(w, "  %-10s %s\n", p.Name, p.Mode)
			}
		})
	}

	cmd.AddCommand(newEdidSetCmd(g))
	cmd.AddCommand(newEdidValidateCmd(g))
	cmd.AddCommand(newEdidDumpCmd(g))
	return cmd
}

func newEdidSetCmd(g *globals) *cobra.Command {
	var file string
	var force bool
	var settle time.Duration
	cmd := &cobra.Command{
		Use:           "set <profile>|--file PATH",
		Short:         "Flash an EDID profile or file, then read back and check capture",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVar(&file, "file", "", "path to a raw EDID hex file to flash instead of a named profile")
	cmd.Flags().BoolVar(&force, "force", false, "flash even when the mode fails the capture check")
	cmd.Flags().DurationVar(&settle, "settle", edidDefaultSettle, "how long to wait for the capture to settle after flashing")
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if file != "" {
			if len(args) > 0 {
				return usagef("edid set takes a profile name or --file, not both")
			}
			return nil
		}
		if len(args) != 1 {
			return usagef("edid set requires a profile name or --file PATH")
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		profile := ""
		if len(args) > 0 {
			profile = args[0]
		}
		result, err := doEdidSet(cmd.Context(), c, profile, file, force, settle)
		if err != nil {
			return err
		}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintf(w, "profile: %s\n", result.Profile)
			fmt.Fprintf(w, "changed: %v\n", result.Changed)
			fmt.Fprintf(w, "mode: %s\n", result.Mode)
			fmt.Fprintf(w, "capture: %s\n", result.Capture)
		})
	}
	return cmd
}

func newEdidValidateCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "validate <file>",
		Short:         "Offline-check an EDID hex file's mode against the capture rules",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return usagef("edid validate takes exactly one file argument, got %d", len(args))
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		result, err := doEdidValidate(args[0])
		if err != nil {
			return err
		}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintf(w, "mode: %s\n", result.Mode)
			fmt.Fprintf(w, "capture: %s\n", result.Capture)
		})
	}
	return cmd
}

func newEdidDumpCmd(g *globals) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:           "dump",
		Short:         "Write the device's currently flashed EDID hex",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVar(&file, "file", "", "output file path, or - for stdout (default stdout)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		hexStr, err := c.GetEDID(cmd.Context())
		if err != nil {
			return err
		}

		result := edidDumpResult{EDID: hexStr}
		if file != "" && file != "-" {
			if err := os.WriteFile(file, []byte(hexStr+"\n"), 0o644); err != nil {
				return fmt.Errorf("write edid to %s: %w", file, err)
			}
			result.File = file
		}

		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			if result.File != "" {
				fmt.Fprintf(w, "wrote %s\n", result.File)
			} else {
				fmt.Fprintln(w, result.EDID)
			}
		})
	}
	return cmd
}
