package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
)

// screenshotResult is the result of glkvm screenshot.
type screenshotResult struct {
	File       string    `json:"file"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	Format     string    `json:"format"`
	Bytes      int       `json:"bytes"`
	CapturedAt time.Time `json:"captured_at"`
}

// waitForSignal checks Streamer().Valid(), retrying every waitPollInterval
// until it succeeds or waitSignal expires. waitSignal <= 0 means check once,
// no retry.
func waitForSignal(ctx context.Context, c *kvmd.Client, waitSignal time.Duration) (kvmd.StreamerState, error) {
	if waitSignal <= 0 {
		st, err := c.Streamer(ctx)
		if err != nil {
			return kvmd.StreamerState{}, err
		}
		if err := st.Valid(); err != nil {
			return kvmd.StreamerState{}, err
		}
		return st, nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, waitSignal)
	defer cancel()

	var last kvmd.StreamerState
	var lastErr error
	err := waitFor(waitCtx, waitPollInterval, func() (bool, error) {
		st, err := c.Streamer(waitCtx)
		if err != nil {
			return false, err
		}
		last = st
		lastErr = st.Valid()
		return lastErr == nil, nil
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && lastErr != nil {
			return kvmd.StreamerState{}, lastErr
		}
		return kvmd.StreamerState{}, err
	}
	return last, nil
}

// doScreenshot checks the capture is valid, fetches a snapshot, and
// optionally converts it to PNG. It returns the result metadata and the
// image bytes to write; the caller decides where those bytes go. It is a
// plain function, not a cobra RunE, so other callers (like a future batch
// runner) can use it without cobra.
func doScreenshot(ctx context.Context, c *kvmd.Client, filePath string, toPNG bool, waitSignal time.Duration) (screenshotResult, []byte, error) {
	st, err := waitForSignal(ctx, c, waitSignal)
	if err != nil {
		return screenshotResult{}, nil, err
	}

	raw, err := c.Snapshot(ctx)
	if err != nil {
		return screenshotResult{}, nil, err
	}

	data := raw
	format := "jpeg"
	if toPNG {
		img, err := jpeg.Decode(bytes.NewReader(raw))
		if err != nil {
			return screenshotResult{}, nil, fmt.Errorf("decode snapshot jpeg: %w", err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return screenshotResult{}, nil, fmt.Errorf("encode snapshot png: %w", err)
		}
		data = buf.Bytes()
		format = "png"
	}

	return screenshotResult{
		File:       filePath,
		Width:      st.Width,
		Height:     st.Height,
		Format:     format,
		Bytes:      len(data),
		CapturedAt: time.Now(),
	}, data, nil
}

// isTerminal reports whether w is a character device, such as an
// interactive terminal, rather than a file, pipe, or in-process buffer.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func newScreenshotCmd(g *globals) *cobra.Command {
	var file string
	var toPNG bool
	var waitSignal time.Duration
	cmd := &cobra.Command{
		Use:           "screenshot",
		Short:         "Capture a screenshot from the streamer",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVar(&file, "file", "", "output file path, or - for stdout")
	cmd.Flags().BoolVar(&toPNG, "png", false, "convert the snapshot to PNG")
	cmd.Flags().DurationVar(&waitSignal, "wait-signal", 0, "retry until the capture is valid or this duration expires")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		toStdout := file == "" || file == "-"
		if toStdout {
			if g.output == "json" {
				return usagef("--file - (or no --file) cannot be combined with -o json: stdout carries the json envelope")
			}
			if isTerminal(cmd.OutOrStdout()) {
				return usagef("refusing to write image bytes to a terminal, pass --file")
			}
		}

		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}

		result, data, err := doScreenshot(cmd.Context(), c, file, toPNG, waitSignal)
		if err != nil {
			return err
		}

		if toStdout {
			if _, err := cmd.OutOrStdout().Write(data); err != nil {
				return fmt.Errorf("write screenshot to stdout: %w", err)
			}
			return nil
		}

		if err := os.WriteFile(file, data, 0o644); err != nil {
			return fmt.Errorf("write screenshot to %s: %w", file, err)
		}

		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintf(w, "wrote %s (%dx%d %s, %d bytes)\n", result.File, result.Width, result.Height, result.Format, result.Bytes)
		})
	}
	return cmd
}
