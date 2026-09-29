package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/txomon/glinet-pikvm-cli/internal/coords"
	"github.com/txomon/glinet-pikvm-cli/internal/keys"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
)

// defaultDragSteps is the default number of linear moves "mouse drag" makes
// between its start and end point.
const defaultDragSteps = 10

// mouseDeltaMin and mouseDeltaMax mirror the kvmd relative mouse range
// checked by kvmd.checkMouseDelta. Checking it again here, before any HID
// call, turns a bad delta into a UsageError instead of the plain error a
// device-classified failure from the kvmd package would give.
const (
	mouseDeltaMin = -127
	mouseDeltaMax = 127
)

// defaultObserveDelay is how long finishAction (and the "run" batch runner's
// --observe-after) waits after the last action before its post-action
// screenshot. The streamer's capture lags the actual device state by about
// one frame, so a screenshot taken immediately after e.g. a keypress can
// still show the pre-action screen. --observe-delay 0 disables the wait.
const defaultObserveDelay = 300 * time.Millisecond

// actionResult is the result of a key, type or mouse command: how many HID
// actions it sent, and the screenshot taken afterward with --file, if any.
type actionResult struct {
	Actions    int               `json:"actions"`
	Screenshot *screenshotResult `json:"screenshot,omitempty"`
}

// atoi parses s as a positional integer argument named name, as a UsageError
// on failure.
func atoi(name, s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, usagef("%s must be an integer, got %q", name, s)
	}
	return n, nil
}

// finishAction optionally takes a screenshot to file, reusing doScreenshot,
// then renders the shared {"actions":N[,"screenshot":...]} result. It is the
// common tail of every key/type/mouse subcommand's RunE. With file set, it
// first waits delay (see defaultObserveDelay) for the capture to catch up to
// the action just sent. The action itself has already succeeded by the time
// finishAction is called, so any failure here (the delay's context, the
// screenshot, or writing it) is reported as the action having completed but
// the screenshot having failed, instead of a bare error that reads like
// nothing happened and invites a rerun that repeats the action (e.g. types
// the text twice).
func finishAction(cmd *cobra.Command, g *globals, c *kvmd.Client, actions int, file string, delay time.Duration) error {
	result := actionResult{Actions: actions}
	if file != "" {
		screenshotFailed := func(err error) error {
			return fmt.Errorf("action completed (%d actions), but the screenshot failed: %w", actions, err)
		}
		if err := sleepCtx(cmd.Context(), delay); err != nil {
			return screenshotFailed(err)
		}
		shot, data, err := doScreenshot(cmd.Context(), c, file, false, 0)
		if err != nil {
			return screenshotFailed(err)
		}
		if err := os.WriteFile(file, data, 0o644); err != nil {
			return screenshotFailed(fmt.Errorf("write screenshot to %s: %w", file, err))
		}
		result.Screenshot = &shot
	}
	return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
		fmt.Fprintf(w, "actions: %d\n", result.Actions)
		if result.Screenshot != nil {
			s := result.Screenshot
			fmt.Fprintf(w, "wrote %s (%dx%d %s, %d bytes)\n", s.File, s.Width, s.Height, s.Format, s.Bytes)
		}
	})
}

// --- key ---

// doKey parses every combo before sending any HID call, so a bad combo
// anywhere in the list never produces a partial action. Each parsed combo of
// one key uses SendKey; more than one key uses SendShortcut. With hold > 0,
// every combo instead presses its keys in order, sleeps for hold, and
// releases them in reverse, regardless of how many keys it has.
func doKey(ctx context.Context, c *kvmd.Client, combos []string, hold time.Duration) error {
	parsed := make([][]string, len(combos))
	for i, combo := range combos {
		ks, err := keys.ParseCombo(combo)
		if err != nil {
			return usagef("%v", err)
		}
		parsed[i] = ks
	}

	for _, ks := range parsed {
		if hold > 0 {
			if err := pressHold(ctx, c, ks, hold); err != nil {
				return err
			}
			continue
		}
		if len(ks) == 1 {
			if err := c.SendKey(ctx, ks[0]); err != nil {
				return err
			}
			continue
		}
		if err := c.SendShortcut(ctx, ks); err != nil {
			return err
		}
	}
	return nil
}

// pressHold presses each key in ks in order, sleeps for hold, then releases
// them in reverse. If a press fails, it releases whatever was already
// pressed (also in reverse) before returning, joining a release failure into
// the returned error instead of discarding it.
func pressHold(ctx context.Context, c *kvmd.Client, ks []string, hold time.Duration) error {
	for i, k := range ks {
		if err := c.KeyState(ctx, k, true); err != nil {
			if relErr := releaseKeys(ctx, c, ks[:i]); relErr != nil {
				return errors.Join(err, relErr)
			}
			return err
		}
	}
	time.Sleep(hold)
	return releaseKeys(ctx, c, ks)
}

// releaseKeys releases ks in reverse order, joining every release failure
// rather than stopping at the first.
func releaseKeys(ctx context.Context, c *kvmd.Client, ks []string) error {
	var errs []error
	for i := len(ks) - 1; i >= 0; i-- {
		if err := c.KeyState(ctx, ks[i], false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func newKeyCmd(g *globals) *cobra.Command {
	var hold time.Duration
	var file string
	var observeDelay time.Duration
	cmd := &cobra.Command{
		Use:           "key <combo>...",
		Short:         "Send one or more key combos (e.g. ctrl+alt+del)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return usagef("key requires at least one combo")
		}
		return nil
	}
	cmd.Flags().DurationVar(&hold, "hold", 0, "press and hold each combo's keys for this duration before releasing")
	cmd.Flags().StringVar(&file, "file", "", "take a screenshot after the action")
	cmd.Flags().DurationVar(&observeDelay, "observe-delay", defaultObserveDelay, "delay before the --file screenshot, letting the capture catch up (0 to disable)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		if err := doKey(cmd.Context(), c, args, hold); err != nil {
			return err
		}
		return finishAction(cmd, g, c, len(args), file, observeDelay)
	}
	return cmd
}

// --- type ---

// doType sends text as raw key events. It is a plain function, not a cobra
// RunE, so other callers (like a future batch runner) can use it without
// cobra.
func doType(ctx context.Context, c *kvmd.Client, text string, slow bool, keymap string) error {
	return c.Print(ctx, text, slow, keymap)
}

func newTypeCmd(g *globals) *cobra.Command {
	var stdin bool
	var slow bool
	var keymap string
	var file string
	var observeDelay time.Duration
	cmd := &cobra.Command{
		Use:           "type <text>",
		Short:         "Type text as raw key events",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read the text to type from stdin instead of an argument")
	cmd.Flags().BoolVar(&slow, "slow", false, "type slowly")
	cmd.Flags().StringVar(&keymap, "keymap", "en-us", "keymap to type with")
	cmd.Flags().StringVar(&file, "file", "", "take a screenshot after the action")
	cmd.Flags().DurationVar(&observeDelay, "observe-delay", defaultObserveDelay, "delay before the --file screenshot, letting the capture catch up (0 to disable)")
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if stdin {
			if len(args) != 0 {
				return usagef("type --stdin takes no text argument")
			}
			return nil
		}
		if len(args) != 1 {
			return usagef("type requires exactly one text argument, or --stdin")
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		text := ""
		if stdin {
			b, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			text = string(b)
		} else {
			text = args[0]
		}

		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		if err := doType(cmd.Context(), c, text, slow, keymap); err != nil {
			return err
		}
		return finishAction(cmd, g, c, 1, file, observeDelay)
	}
	return cmd
}

// --- mouse: shared coordinate scaling ---

// parseSize parses a "WxH" string, as a UsageError on any malformed or
// non-positive value.
func parseSize(s string) (w, h int, err error) {
	parts := strings.SplitN(s, "x", 2)
	if len(parts) != 2 {
		return 0, 0, usagef("--size must be WxH, got %q", s)
	}
	w, errW := strconv.Atoi(parts[0])
	h, errH := strconv.Atoi(parts[1])
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return 0, 0, usagef("--size must be WxH with positive integers, got %q", s)
	}
	return w, h, nil
}

// resolveSize returns the capture size to scale pixel coordinates against:
// sizeFlag parsed as "WxH" when non-empty, otherwise the streamer's current
// capture size, which must be valid. An invalid capture is a device error
// naming why; --size skips the device lookup entirely.
func resolveSize(ctx context.Context, c *kvmd.Client, sizeFlag string) (w, h int, err error) {
	if sizeFlag != "" {
		return parseSize(sizeFlag)
	}
	st, err := c.Streamer(ctx)
	if err != nil {
		return 0, 0, err
	}
	if verr := st.Valid(); verr != nil {
		return 0, 0, fmt.Errorf("cannot scale coordinates without a capture signal: %w", verr)
	}
	return st.Width, st.Height, nil
}

// scalePixel converts pixel coordinates (x,y) on a capture of size wxh into
// the kvmd absolute mouse range, or a UsageError naming the resolution when
// out of bounds.
func scalePixel(x, y, w, h int) (ax, ay int, err error) {
	if x < 0 || x >= w || y < 0 || y >= h {
		return 0, 0, usagef("coordinates (%d,%d) out of bounds for capture %dx%d", x, y, w, h)
	}
	ax, err = coords.ToAbsolute(x, w)
	if err != nil {
		return 0, 0, fmt.Errorf("scale x: %w", err)
	}
	ay, err = coords.ToAbsolute(y, h)
	if err != nil {
		return 0, 0, fmt.Errorf("scale y: %w", err)
	}
	return ax, ay, nil
}

// validButton rejects any mouse button name other than left, right or
// middle, as a UsageError.
func validButton(button string) error {
	switch button {
	case "left", "right", "middle":
		return nil
	default:
		return usagef("mouse button must be left, right or middle, got %q", button)
	}
}

// validDelta rejects a relative mouse delta outside kvmd's -127..127 range,
// as a UsageError, before any HID call is sent.
func validDelta(name string, d int) error {
	if d < mouseDeltaMin || d > mouseDeltaMax {
		return usagef("%s must be between %d and %d, got %d", name, mouseDeltaMin, mouseDeltaMax, d)
	}
	return nil
}

// --- mouse: actions ---

// doMouseMove scales (x,y) against the resolved capture size wxh and moves
// the mouse there.
func doMouseMove(ctx context.Context, c *kvmd.Client, x, y, w, h int) error {
	ax, ay, err := scalePixel(x, y, w, h)
	if err != nil {
		return err
	}
	return c.MouseMove(ctx, ax, ay)
}

// doMouseClick moves the mouse to (x,y) and clicks button there.
func doMouseClick(ctx context.Context, c *kvmd.Client, x, y, w, h int, button string) error {
	if err := validButton(button); err != nil {
		return err
	}
	ax, ay, err := scalePixel(x, y, w, h)
	if err != nil {
		return err
	}
	if err := c.MouseMove(ctx, ax, ay); err != nil {
		return err
	}
	return c.MouseButton(ctx, button)
}

// doMouseDoubleClick moves the mouse to (x,y) and clicks the left button
// twice there.
func doMouseDoubleClick(ctx context.Context, c *kvmd.Client, x, y, w, h int) error {
	ax, ay, err := scalePixel(x, y, w, h)
	if err != nil {
		return err
	}
	if err := c.MouseMove(ctx, ax, ay); err != nil {
		return err
	}
	if err := c.MouseButton(ctx, "left"); err != nil {
		return err
	}
	return c.MouseButton(ctx, "left")
}

// doMouseDrag moves to (x1,y1), presses the left button, moves in steps
// linear steps to (x2,y2), then releases. If a step after the press fails,
// it still attempts the release before returning, joining a release failure
// into the returned error instead of discarding it.
func doMouseDrag(ctx context.Context, c *kvmd.Client, x1, y1, x2, y2, w, h, steps int) error {
	if steps < 1 {
		return usagef("drag --steps must be at least 1, got %d", steps)
	}
	ax1, ay1, err := scalePixel(x1, y1, w, h)
	if err != nil {
		return err
	}
	ax2, ay2, err := scalePixel(x2, y2, w, h)
	if err != nil {
		return err
	}

	if err := c.MouseMove(ctx, ax1, ay1); err != nil {
		return err
	}
	if err := c.MouseButtonState(ctx, "left", true); err != nil {
		return err
	}

	for i := 1; i <= steps; i++ {
		frac := float64(i) / float64(steps)
		sx := ax1 + int(math.Round(float64(ax2-ax1)*frac))
		sy := ay1 + int(math.Round(float64(ay2-ay1)*frac))
		if err := c.MouseMove(ctx, sx, sy); err != nil {
			if relErr := c.MouseButtonState(ctx, "left", false); relErr != nil {
				return errors.Join(err, relErr)
			}
			return err
		}
	}

	return c.MouseButtonState(ctx, "left", false)
}

// doMouseScroll scrolls the wheel by (dx,dy).
func doMouseScroll(ctx context.Context, c *kvmd.Client, dx, dy int) error {
	if err := validDelta("dx", dx); err != nil {
		return err
	}
	if err := validDelta("dy", dy); err != nil {
		return err
	}
	return c.MouseWheel(ctx, dx, dy)
}

// doMouseNudge moves the mouse by the relative delta (dx,dy). It reads the
// current HID state first and fails, before sending anything, when the
// mouse output is absolute: a relative move is silently ignored there, which
// would otherwise look like a successful no-op.
func doMouseNudge(ctx context.Context, c *kvmd.Client, dx, dy int) error {
	if dx == 0 && dy == 0 {
		return usagef("nudge requires --dx or --dy to be non-zero")
	}
	if err := validDelta("dx", dx); err != nil {
		return err
	}
	if err := validDelta("dy", dy); err != nil {
		return err
	}
	hid, err := c.HID(ctx)
	if err != nil {
		return err
	}
	if hid.MouseAbsolute {
		return fmt.Errorf("mouse output is absolute; relative moves are ignored; use mouse move, or switch the mouse output to usb_rel or usb_hybrid")
	}
	return c.MouseRelative(ctx, dx, dy)
}

// --- mouse: cobra wiring ---

func newMouseCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "mouse",
		Short:         "Move, click, drag, scroll or nudge the mouse",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// A group command with no RunE is "not runnable": cobra would silently
	// print help and exit 0 for an unrecognized subcommand, same quirk noted
	// in newRoot. A trivial RunE keeps it runnable so noArgs still rejects a
	// stray argument.
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	}

	cmd.AddCommand(newMouseMoveCmd(g))
	cmd.AddCommand(newMouseClickCmd(g))
	cmd.AddCommand(newMouseDoubleClickCmd(g))
	cmd.AddCommand(newMouseDragCmd(g))
	cmd.AddCommand(newMouseScrollCmd(g))
	cmd.AddCommand(newMouseNudgeCmd(g))
	return cmd
}

func exactArgs(name string, want int, names ...string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != want {
			return usagef("%s requires %s, got %d argument(s)", name, strings.Join(names, " "), len(args))
		}
		return nil
	}
}

func newMouseMoveCmd(g *globals) *cobra.Command {
	var size, file string
	var observeDelay time.Duration
	cmd := &cobra.Command{
		Use:           "move X Y",
		Short:         "Move the mouse to a screenshot pixel coordinate",
		Args:          exactArgs("mouse move", 2, "X", "Y"),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVar(&size, "size", "", "override the capture size as WxH instead of querying the device")
	cmd.Flags().StringVar(&file, "file", "", "take a screenshot after the action")
	cmd.Flags().DurationVar(&observeDelay, "observe-delay", defaultObserveDelay, "delay before the --file screenshot, letting the capture catch up (0 to disable)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		x, err := atoi("X", args[0])
		if err != nil {
			return err
		}
		y, err := atoi("Y", args[1])
		if err != nil {
			return err
		}
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		w, h, err := resolveSize(cmd.Context(), c, size)
		if err != nil {
			return err
		}
		if err := doMouseMove(cmd.Context(), c, x, y, w, h); err != nil {
			return err
		}
		return finishAction(cmd, g, c, 1, file, observeDelay)
	}
	return cmd
}

func newMouseClickCmd(g *globals) *cobra.Command {
	var size, file, button string
	var observeDelay time.Duration
	cmd := &cobra.Command{
		Use:           "click X Y",
		Short:         "Move the mouse to a screenshot pixel coordinate and click",
		Args:          exactArgs("mouse click", 2, "X", "Y"),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVar(&size, "size", "", "override the capture size as WxH instead of querying the device")
	cmd.Flags().StringVar(&file, "file", "", "take a screenshot after the action")
	cmd.Flags().StringVar(&button, "button", "left", "mouse button: left, right or middle")
	cmd.Flags().DurationVar(&observeDelay, "observe-delay", defaultObserveDelay, "delay before the --file screenshot, letting the capture catch up (0 to disable)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		x, err := atoi("X", args[0])
		if err != nil {
			return err
		}
		y, err := atoi("Y", args[1])
		if err != nil {
			return err
		}
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		w, h, err := resolveSize(cmd.Context(), c, size)
		if err != nil {
			return err
		}
		if err := doMouseClick(cmd.Context(), c, x, y, w, h, button); err != nil {
			return err
		}
		return finishAction(cmd, g, c, 1, file, observeDelay)
	}
	return cmd
}

func newMouseDoubleClickCmd(g *globals) *cobra.Command {
	var size, file string
	var observeDelay time.Duration
	cmd := &cobra.Command{
		Use:           "double-click X Y",
		Short:         "Move the mouse to a screenshot pixel coordinate and double-click",
		Args:          exactArgs("mouse double-click", 2, "X", "Y"),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVar(&size, "size", "", "override the capture size as WxH instead of querying the device")
	cmd.Flags().StringVar(&file, "file", "", "take a screenshot after the action")
	cmd.Flags().DurationVar(&observeDelay, "observe-delay", defaultObserveDelay, "delay before the --file screenshot, letting the capture catch up (0 to disable)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		x, err := atoi("X", args[0])
		if err != nil {
			return err
		}
		y, err := atoi("Y", args[1])
		if err != nil {
			return err
		}
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		w, h, err := resolveSize(cmd.Context(), c, size)
		if err != nil {
			return err
		}
		if err := doMouseDoubleClick(cmd.Context(), c, x, y, w, h); err != nil {
			return err
		}
		return finishAction(cmd, g, c, 1, file, observeDelay)
	}
	return cmd
}

func newMouseDragCmd(g *globals) *cobra.Command {
	var size, file string
	var steps int
	var observeDelay time.Duration
	cmd := &cobra.Command{
		Use:           "drag X1 Y1 X2 Y2",
		Short:         "Press the left button at (X1,Y1), drag to (X2,Y2), and release",
		Args:          exactArgs("mouse drag", 4, "X1", "Y1", "X2", "Y2"),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVar(&size, "size", "", "override the capture size as WxH instead of querying the device")
	cmd.Flags().StringVar(&file, "file", "", "take a screenshot after the action")
	cmd.Flags().IntVar(&steps, "steps", defaultDragSteps, "number of linear moves from start to end")
	cmd.Flags().DurationVar(&observeDelay, "observe-delay", defaultObserveDelay, "delay before the --file screenshot, letting the capture catch up (0 to disable)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		x1, err := atoi("X1", args[0])
		if err != nil {
			return err
		}
		y1, err := atoi("Y1", args[1])
		if err != nil {
			return err
		}
		x2, err := atoi("X2", args[2])
		if err != nil {
			return err
		}
		y2, err := atoi("Y2", args[3])
		if err != nil {
			return err
		}
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		w, h, err := resolveSize(cmd.Context(), c, size)
		if err != nil {
			return err
		}
		if err := doMouseDrag(cmd.Context(), c, x1, y1, x2, y2, w, h, steps); err != nil {
			return err
		}
		return finishAction(cmd, g, c, 1, file, observeDelay)
	}
	return cmd
}

// defaultScrollAmount is "mouse scroll"'s wheel delta when N is omitted.
const defaultScrollAmount = 3

// directionDelta turns a scroll direction and a positive amount n into the
// signed (dx,dy) doMouseScroll takes, as a UsageError for any direction
// other than up, down, left or right.
//
// This follows the kvmd/evdev wheel convention, not screen-coordinate sign:
// a positive REL_WHEEL (delta_y) scrolls the content up, so "up" is
// positive and "down" is negative. Confirmed against a real device (a
// positive delta_y arrived as scroll-up) and against upstream's own web UI
// (reference/kvmd/web/share/js/kvm/mouse.js, __sendScroll: a browser
// scroll-down, wheel.deltaY > 0, is sent as a negative delta_y). Horizontal
// stays screen-oriented: "left" is negative delta_x, "right" is positive.
func directionDelta(direction string, n int) (dx, dy int, err error) {
	switch direction {
	case "up":
		return 0, n, nil
	case "down":
		return 0, -n, nil
	case "left":
		return -n, 0, nil
	case "right":
		return n, 0, nil
	default:
		return 0, 0, usagef("mouse scroll direction must be up, down, left or right, got %q", direction)
	}
}

func newMouseScrollCmd(g *globals) *cobra.Command {
	var file string
	var observeDelay time.Duration
	cmd := &cobra.Command{
		Use:           "scroll up|down|left|right [N]",
		Short:         "Scroll the mouse wheel in a direction",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 || len(args) > 2 {
			return usagef("mouse scroll requires a direction (up, down, left or right) and an optional N, got %d argument(s)", len(args))
		}
		return nil
	}
	cmd.Flags().StringVar(&file, "file", "", "take a screenshot after the action")
	cmd.Flags().DurationVar(&observeDelay, "observe-delay", defaultObserveDelay, "delay before the --file screenshot, letting the capture catch up (0 to disable)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		n := defaultScrollAmount
		if len(args) == 2 {
			v, err := atoi("N", args[1])
			if err != nil {
				return err
			}
			n = v
		}
		if n <= 0 {
			return usagef("N must be a positive integer, got %d", n)
		}
		dx, dy, err := directionDelta(args[0], n)
		if err != nil {
			return err
		}
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		if err := doMouseScroll(cmd.Context(), c, dx, dy); err != nil {
			return err
		}
		return finishAction(cmd, g, c, 1, file, observeDelay)
	}
	return cmd
}

func newMouseNudgeCmd(g *globals) *cobra.Command {
	var dx, dy int
	var file string
	var observeDelay time.Duration
	cmd := &cobra.Command{
		Use:           "nudge --dx N --dy N",
		Short:         "Move the mouse by a relative delta",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().IntVar(&dx, "dx", 0, "horizontal relative delta, -127 to 127")
	cmd.Flags().IntVar(&dy, "dy", 0, "vertical relative delta, -127 to 127")
	cmd.Flags().StringVar(&file, "file", "", "take a screenshot after the action")
	cmd.Flags().DurationVar(&observeDelay, "observe-delay", defaultObserveDelay, "delay before the --file screenshot, letting the capture catch up (0 to disable)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		if err := doMouseNudge(cmd.Context(), c, dx, dy); err != nil {
			return err
		}
		return finishAction(cmd, g, c, 1, file, observeDelay)
	}
	return cmd
}
