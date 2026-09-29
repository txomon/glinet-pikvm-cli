package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
)

// defaultSettle is how long "port" waits, after a switch, for the target
// port to become active and its capture to settle.
const defaultSettle = 10 * time.Second

// portResult is the result of glkvm port, both for a plain show and after a
// switch. Changed is only meaningful after next/prev: the device does not
// wrap (verified live: next from 1.4 and prev from 1.1 are no-ops), so
// asking to advance past either end leaves the active port unchanged.
type portResult struct {
	Active   int        `json:"active"`
	ActiveID string     `json:"active_id"`
	Changed  bool       `json:"changed"`
	Ports    []portLink `json:"ports"`
	Capture  struct {
		Online     bool   `json:"online"`
		Resolution string `json:"resolution"`
	} `json:"capture"`
}

// portArgKind is what glkvm port was asked to do.
type portArgKind int

const (
	portShow portArgKind = iota
	portSwitch
	portNext
	portPrev
)

// parsePortArg classifies the single positional argument to "port". An
// empty arg means "show current state". Anything other than 1..4,
// "next" or "prev" is a UsageError.
func parsePortArg(arg string) (portArgKind, int, error) {
	switch arg {
	case "":
		return portShow, 0, nil
	case "next":
		return portNext, 0, nil
	case "prev":
		return portPrev, 0, nil
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > 4 {
		return 0, 0, usagef("port must be 1 to 4, or next/prev; got %q", arg)
	}
	return portSwitch, n, nil
}

// buildPortResult reads the current switch and streamer state into a
// portResult, without switching anything.
func buildPortResult(ctx context.Context, c *kvmd.Client) (portResult, error) {
	sw, err := c.Switch(ctx)
	if err != nil {
		return portResult{}, err
	}
	st, err := c.Streamer(ctx)
	if err != nil {
		return portResult{}, err
	}

	var result portResult
	result.Active = sw.ActivePort + 1
	result.ActiveID = sw.ActiveID
	result.Ports = portLinks(sw)
	result.Capture.Online = st.Online
	result.Capture.Resolution = st.RealResolution
	return result, nil
}

// findPortLink returns the index of the port whose ID is id.
func findPortLink(ports []portLink, id string) (int, bool) {
	for i, p := range ports {
		if p.ID == id {
			return i, true
		}
	}
	return 0, false
}

// doPort shows or switches the active host port. With arg == "" it only
// reports state. Otherwise it switches (1..4, "next" or "prev"), then waits
// up to settle for the target port to become active with a valid capture.
// A settle expiry is only an error when the target port's HDMI link is up;
// with no HDMI link at all it is expected and reported as no signal, not an
// error. It is a plain function, not a cobra RunE, so other callers (like a
// future batch runner) can use it without cobra.
//
// next/prev do not wrap on the real device (verified live: next from 1.4
// and prev from 1.1 are no-ops), so the active port before the call is
// captured first; when it comes back unchanged after the POST, that is
// reported as such (Changed: false, exit 0) instead of waiting out the full
// settle duration for a switch that was never going to happen.
func doPort(ctx context.Context, c *kvmd.Client, arg string, settle time.Duration) (portResult, error) {
	kind, n, err := parsePortArg(arg)
	if err != nil {
		return portResult{}, err
	}
	if kind == portShow {
		return buildPortResult(ctx, c)
	}

	var beforeID string
	if kind == portNext || kind == portPrev {
		sw, err := c.Switch(ctx)
		if err != nil {
			return portResult{}, err
		}
		beforeID = sw.ActiveID
	}

	targetID := fmt.Sprintf("1.%d", n)
	switch kind {
	case portSwitch:
		if err := c.SetActivePort(ctx, n); err != nil {
			return portResult{}, err
		}
	case portNext:
		if err := c.SetActiveNext(ctx); err != nil {
			return portResult{}, err
		}
	case portPrev:
		if err := c.SetActivePrev(ctx); err != nil {
			return portResult{}, err
		}
	}

	noop := false
	if kind != portSwitch {
		sw, err := c.Switch(ctx)
		if err != nil {
			return portResult{}, err
		}
		targetID = sw.ActiveID
		noop = targetID == beforeID
	}

	if noop {
		result, err := buildPortResult(ctx, c)
		if err != nil {
			return portResult{}, err
		}
		result.Changed = false
		return result, nil
	}

	settleCtx, cancel := context.WithTimeout(ctx, settle)
	defer cancel()

	waitErr := waitFor(settleCtx, waitPollInterval, func() (bool, error) {
		sw, err := c.Switch(settleCtx)
		if err != nil {
			return false, err
		}
		if sw.ActiveID != targetID {
			return false, nil
		}
		st, err := c.Streamer(settleCtx)
		if err != nil {
			return false, err
		}
		return st.Valid() == nil, nil
	})

	result, err := buildPortResult(ctx, c)
	if err != nil {
		return portResult{}, err
	}
	result.Changed = true

	if waitErr != nil {
		if !errors.Is(waitErr, context.DeadlineExceeded) {
			return portResult{}, waitErr
		}
		idx, found := findPortLink(result.Ports, targetID)
		if !found || result.Ports[idx].HDMI {
			return portResult{}, fmt.Errorf("port %s did not settle within %s: capture %q", targetID, settle, result.Capture.Resolution)
		}
	}

	return result, nil
}

func newPortCmd(g *globals) *cobra.Command {
	var settle time.Duration
	cmd := &cobra.Command{
		Use:           "port [N|next|prev]",
		Short:         "Show or switch the active host port",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) > 1 {
			return usagef("port takes at most one argument, got %d", len(args))
		}
		return nil
	}
	cmd.Flags().DurationVar(&settle, "settle", defaultSettle, "how long to wait for the port to settle after a switch")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		arg := ""
		if len(args) > 0 {
			arg = args[0]
		}
		result, err := doPort(cmd.Context(), c, arg, settle)
		if err != nil {
			return err
		}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintf(w, "active: %d (%s)\n", result.Active, result.ActiveID)
			if !result.Changed {
				switch arg {
				case "next":
					fmt.Fprintln(w, "already at the last port")
				case "prev":
					fmt.Fprintln(w, "already at the first port")
				}
			}
			for _, p := range result.Ports {
				fmt.Fprintf(w, "  port %d %s: hdmi=%v usb=%v\n", p.Port, p.ID, p.HDMI, p.USB)
			}
			if result.Capture.Online {
				fmt.Fprintf(w, "capture: %s\n", result.Capture.Resolution)
			} else {
				fmt.Fprintf(w, "capture: no signal\n")
			}
		})
	}
	return cmd
}
