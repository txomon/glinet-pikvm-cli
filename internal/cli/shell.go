package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
)

// nonInteractiveCols and nonInteractiveRows are the fixed terminal size
// requested by shell -c, which has no real terminal to query. Wide and
// short-ish rather than a plain 80x24: live-confirmed the remote otherwise
// runs -c on an 80x24 pty, and a narrow terminal wraps long output lines,
// which the marker scanner (and, more importantly, a caller reading the
// output) sees as extra line breaks that were never in the command's own
// output.
const (
	nonInteractiveCols = 500
	nonInteractiveRows = 50
)

// webtermConn is the subset of *kvmd.WebtermConn the shell command needs.
// Depending on this narrow interface, instead of the concrete type, lets
// shellRelay and runShellCommand be tested against an in-memory fake
// instead of a real websocket connection.
type webtermConn interface {
	io.Writer
	Resize(cols, rows int) error
	ReadOutput(ctx context.Context) ([]byte, error)
	Close() error
}

// errShellEscape is returned by shellRelay when the user pressed Ctrl-]
// (ctrlCloseByte) to close the session locally, distinguishing a
// deliberate local escape from the remote shell exiting or local input
// closing (both of which shellRelay reports as a plain nil, not this).
var errShellEscape = errors.New("shell: closed by Ctrl-]")

// ctrlCloseByte (0x1d, Ctrl-]) is the traditional terminal escape for
// forcing a session closed locally, without waiting for the remote end
// (which may never respond, e.g. because the network dropped).
const ctrlCloseByte = 0x1d

// shellRelay relays bytes between a local terminal (in, out) and conn,
// forwarding size changes received on resize, until one of three things
// happens: conn.ReadOutput returns io.EOF (the remote shell exited),
// in.Read returns io.EOF (local input closed), or the local input contains
// ctrlCloseByte (the user pressed Ctrl-]; anything read before that byte in
// the same chunk is still forwarded first, and shellRelay then returns
// errShellEscape instead of nil, so the caller can tell the three cases
// apart). warn, if non-nil, receives a short message whenever a Resize call
// fails; a failed resize does not end the session, so it is reported
// rather than silently dropped or treated as fatal.
//
// It has no dependency on a real TTY, so it is testable with plain
// io.Reader/io.Writer values and a resize channel fed by hand.
//
// It returns as soon as any one of the three ends, without waiting for the
// goroutine blocked reading local stdin: in production, once the remote
// shell exits, the interactive command must return promptly, and that
// goroutine's in.Read has no way to be interrupted (a real terminal's stdin
// has no read deadline), so it is left to exit when the process itself
// does. The output and resize goroutines are different: both take a
// context derived from ctx, so before returning, shellRelay cancels it and
// waits for both to exit, which unblocks a resize goroutine parked on the
// resize channel and a ReadOutput call blocked on conn. This guarantees
// neither goroutine is still writing to out or warn once shellRelay has
// returned, which a caller (or test) could otherwise observe as a data
// race.
func shellRelay(ctx context.Context, conn webtermConn, in io.Reader, out io.Writer, resize <-chan [2]int, warn io.Writer) error {
	doneCh := make(chan error, 2)

	relayCtx, relayCancel := context.WithCancel(ctx)
	defer relayCancel()
	var wg sync.WaitGroup

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				if idx := bytes.IndexByte(chunk, ctrlCloseByte); idx >= 0 {
					if idx > 0 {
						if _, werr := conn.Write(chunk[:idx]); werr != nil {
							doneCh <- werr
							return
						}
					}
					doneCh <- errShellEscape
					return
				}
				if _, werr := conn.Write(chunk); werr != nil {
					doneCh <- werr
					return
				}
			}
			if err != nil {
				doneCh <- err
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			data, err := conn.ReadOutput(relayCtx)
			if err != nil {
				doneCh <- err
				return
			}
			if _, werr := out.Write(data); werr != nil {
				doneCh <- werr
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-relayCtx.Done():
				return
			case sz, ok := <-resize:
				if !ok {
					return
				}
				if err := conn.Resize(sz[0], sz[1]); err != nil && warn != nil {
					fmt.Fprintf(warn, "glkvm: shell: resize to %dx%d failed: %v\n", sz[0], sz[1], err)
				}
			}
		}
	}()

	err := <-doneCh
	relayCancel()
	wg.Wait()
	if errors.Is(err, errShellEscape) {
		return errShellEscape
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// scanPhase is markerScanner's progress through one command's output.
type scanPhase int

const (
	phaseFindStart scanPhase = iota
	phaseAwaitStartNL
	phaseCapture
	phaseAwaitEndNL
	phaseDone
)

// markerScanner extracts one command's captured output from a live ttyd
// output stream. Everything before the start marker (the ttyd title escape,
// the motd, echoed input, shell prompts) is discarded; everything between
// the start and end markers is written to out, with CRLF normalized to LF;
// the digits immediately after the end marker are the remote command's exit
// status. Feed handles a marker landing anywhere in the stream, including
// split across separate calls (a marker straddling two websocket frames).
type markerScanner struct {
	start, end string
	out        io.Writer

	buf      []byte
	phase    scanPhase
	exitCode int
}

// newMarkerScanner builds a markerScanner. start and end must be non-empty
// and distinct enough not to appear in the command's own output; the shell
// command generates them as random tokens.
func newMarkerScanner(start, end string, out io.Writer) *markerScanner {
	return &markerScanner{start: start, end: end, out: out}
}

// Feed processes one chunk of raw output, writing newly-recognized captured
// output to out as it is found. It returns done=true once the end marker's
// exit status has been fully parsed; exitCode is only valid then. Feed must
// not be called again after it returns done=true.
func (s *markerScanner) Feed(chunk []byte) (done bool, err error) {
	s.buf = append(s.buf, chunk...)

	for {
		switch s.phase {
		case phaseFindStart:
			idx := bytes.Index(s.buf, []byte(s.start))
			if idx < 0 {
				// Keep only the tail that could still be the beginning of a
				// split start marker; everything else here is noise.
				if keep := len(s.start) - 1; len(s.buf) > keep {
					s.buf = s.buf[len(s.buf)-keep:]
				}
				return false, nil
			}
			s.buf = s.buf[idx+len(s.start):]
			s.phase = phaseAwaitStartNL

		case phaseAwaitStartNL:
			nl := bytes.IndexByte(s.buf, '\n')
			if nl < 0 {
				// Still inside the start marker's own line (echoed input or
				// its own newline has not arrived yet): all noise so far.
				s.buf = nil
				return false, nil
			}
			s.buf = s.buf[nl+1:]
			s.phase = phaseCapture

		case phaseCapture:
			idx := bytes.Index(s.buf, []byte(s.end))
			if idx < 0 {
				// Flush everything except a tail long enough to still hold
				// a split end marker. cutAt additionally backs off one more
				// byte when the proposed cut would land right after a bare
				// '\r': without that, a CRLF pair split exactly at this
				// boundary (the '\r' flushed now, the '\n' arriving in a
				// later chunk) would leak through as a literal '\r'
				// followed by an unconverted '\n' instead of collapsing to
				// one '\n', which is exactly the stray-CR bug seen on a
				// live run. Holding the '\r' back lets a later flush still
				// collapse it once its '\n' arrives.
				cut := cutAt(s.buf, len(s.end))
				if cut > 0 {
					if ferr := s.flush(s.buf[:cut]); ferr != nil {
						return false, ferr
					}
					s.buf = s.buf[cut:]
				}
				return false, nil
			}
			if ferr := s.flush(s.buf[:idx]); ferr != nil {
				return false, ferr
			}
			s.buf = s.buf[idx+len(s.end):]
			s.phase = phaseAwaitEndNL

		case phaseAwaitEndNL:
			nl := bytes.IndexByte(s.buf, '\n')
			if nl < 0 {
				return false, nil
			}
			line := strings.TrimRight(string(s.buf[:nl]), "\r")
			code, cerr := strconv.Atoi(strings.TrimSpace(line))
			if cerr != nil {
				return false, fmt.Errorf("shell: parse remote exit status %q: %w", line, cerr)
			}
			s.exitCode = code
			s.phase = phaseDone
			return true, nil

		case phaseDone:
			return true, nil
		}
	}
}

// flush writes b to out, normalizing CRLF to LF (the remote is a pty).
func (s *markerScanner) flush(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	_, err := s.out.Write(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")))
	return err
}

// cutAt returns how many bytes of buf are safe to flush now, keeping back
// a margin byte tail long enough to still recognize a split end marker,
// and never at a position right after a bare '\r' (see the call site).
func cutAt(buf []byte, margin int) int {
	cut := len(buf) - margin
	if cut > 0 && buf[cut-1] == '\r' {
		cut--
	}
	return cut
}

// randomToken returns a random hex string, used to build marker text that
// will not collide with a command's own output.
func randomToken() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("shell: generate marker token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// shellCommandResult is the result of glkvm shell -c.
type shellCommandResult struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

// markerPrefix is the fixed half of every marker, kept separate from the
// per-run nonce (see buildShellScript) so it never appears as one
// contiguous string in the input glkvm itself sends.
const markerPrefix = "__GLKVM_"

// buildShellScript wraps cmd in a subshell, redirected from /dev/null, on
// its own line between the marker printfs.
//
// The subshell means an "exit" inside cmd ends only that subshell (leaving
// $? as cmd's own status) instead of the whole remote session, which would
// otherwise skip the trailing marker/exit lines. The /dev/null redirect
// means a command that itself reads stdin (e.g. "read x") gets immediate
// EOF instead of consuming the marker and exit lines queued right behind
// it on the same input stream, which live-confirmed hangs the whole run:
// the command would read glkvm's own follow-up lines as if they were its
// input. Putting cmd on its own line (real newlines before and after, not
// appended to the "(" line) means a trailing "# comment" only swallows
// cmd's own line, not the closing ")" on the line after it; a trailing
// unescaped backslash still joins that following line, but "(" and ")" are
// shell metacharacters recognized regardless of surrounding whitespace, so
// the subshell still closes correctly either way.
//
// The remote is a real pty: the terminal echoes input back as it arrives,
// including whatever glkvm is about to send, before "stty -echo" (kept
// below as a best effort) has any chance to take effect. If the marker text
// ever appeared literally in that sent input, the echo of glkvm's own
// script would contain a complete marker, and the scanner (which cannot
// tell an echoed input line from real command output) would match it
// there, well before the real command has even run. To avoid that, each
// marker is never sent as one contiguous string: it is built on the remote
// side, at runtime, by a printf joining two pieces that are passed as
// separate, space-separated words. The joined marker then exists only in
// printf's own output, never in anything glkvm wrote to the socket.
//
// The rest of the preamble line is also live-confirmed necessary: the
// remote runs -c on an interactive bash, which does its own history
// expansion ("!" in a double-quoted string fails with "event not found"
// unless history expansion is off), pages long output through $PAGER
// (hanging a non-interactive run waiting for a keypress that never comes),
// and would otherwise pick up whatever HISTFILE/PROMPT_COMMAND happen to be
// set. set +H disables history expansion; the rest heads off a pager and
// any prompt-time side effects.
//
// "set +H" is bash-specific: a POSIX-only shell (dash, this repo's fake)
// does not recognize the -H option, and since "set" is a POSIX special
// builtin, that syntax error is fatal to a non-interactive shell, killing
// the whole session outright rather than merely failing that one command
// (confirmed against dash: even redirecting its stderr or following it with
// "|| true" does not stop the shell from exiting). Guarding it behind a
// $BASH_VERSION check means it is only ever attempted under bash, where it
// is both valid and needed; a POSIX shell never even reaches the "set +H"
// word, since "&&" short-circuits on the false test.
func buildShellScript(nonce, cmd string) string {
	var b strings.Builder
	b.WriteString(`stty -echo 2>/dev/null; [ -n "$BASH_VERSION" ] && set +H; `)
	b.WriteString("unset HISTFILE PROMPT_COMMAND; ")
	b.WriteString("export PAGER=cat SYSTEMD_PAGER=cat TERM=dumb; PS1=; PS2=\n")
	b.WriteString(`printf '%s%s\n' ` + markerPrefix + " S_" + nonce + "\n")
	b.WriteString("(\n")
	b.WriteString(cmd)
	b.WriteString("\n) </dev/null\n")
	b.WriteString(`printf '%s%s:%d\n' ` + markerPrefix + " E_" + nonce + ` "$?"` + "\n")
	b.WriteString("exit\n")
	return b.String()
}

// runShellCommand runs cmd on the remote shell over conn, writing its
// captured output to out as it arrives, and returns the remote exit
// status. ctx bounds the whole run. captureOutput additionally accumulates
// a copy of the output into the returned result's Output field, for json
// mode's envelope; text mode already streamed it to out as it arrived, so
// it passes false and does not pay for a second, otherwise-unused copy.
func runShellCommand(ctx context.Context, conn webtermConn, cmd string, out io.Writer, captureOutput bool) (shellCommandResult, error) {
	nonce, err := randomToken()
	if err != nil {
		return shellCommandResult{}, err
	}
	// These are the marker strings as they appear in printf's own output
	// (the two pieces joined with no separator): what the scanner searches
	// for. end includes the trailing ':' that separates it from the exit
	// status digits printf appends right after.
	start := markerPrefix + "S_" + nonce
	end := markerPrefix + "E_" + nonce + ":"

	if _, err := conn.Write([]byte(buildShellScript(nonce, cmd))); err != nil {
		return shellCommandResult{}, fmt.Errorf("shell: send command: %w", err)
	}

	sink := out
	var captured bytes.Buffer
	if captureOutput {
		sink = io.MultiWriter(out, &captured)
	}
	scanner := newMarkerScanner(start, end, sink)
	for {
		data, err := conn.ReadOutput(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return shellCommandResult{}, fmt.Errorf("shell: connection closed before the command finished")
			}
			return shellCommandResult{}, fmt.Errorf("shell: %w", err)
		}
		done, err := scanner.Feed(data)
		if err != nil {
			return shellCommandResult{}, err
		}
		if done {
			break
		}
	}
	return shellCommandResult{ExitCode: scanner.exitCode, Output: captured.String()}, nil
}

// fileFD returns v's file descriptor when v is an *os.File.
func fileFD(v any) (uintptr, bool) {
	f, ok := v.(*os.File)
	if !ok {
		return 0, false
	}
	return f.Fd(), true
}

// isInteractiveTerminal reports whether both in and out are real terminals.
func isInteractiveTerminal(in io.Reader, out io.Writer) bool {
	inFD, ok := fileFD(in)
	if !ok || !term.IsTerminal(int(inFD)) {
		return false
	}
	outFD, ok := fileFD(out)
	if !ok || !term.IsTerminal(int(outFD)) {
		return false
	}
	return true
}

// runShellCCommand runs the -c command flow: dial, run one command bounded
// by g.timeout, then either render the json envelope (exit 0, the remote
// status lives in the result) or propagate the remote status as the
// process's own exit code (text mode, like ssh).
func runShellCCommand(cmd *cobra.Command, g *globals, c *kvmd.Client, command string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()

	conn, err := c.Webterm(ctx, nonInteractiveCols, nonInteractiveRows)
	if err != nil {
		return err
	}
	defer conn.Close()

	var out io.Writer = cmd.OutOrStdout()
	captureOutput := g.output == "json"
	if captureOutput {
		out = io.Discard
	}

	result, err := runShellCommand(ctx, conn, command, out, captureOutput)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("shell: command did not finish within %s", g.timeout)
		}
		return err
	}

	if g.output == "json" {
		return render(cmd.OutOrStdout(), g.output, result, nil)
	}
	return ExitCodeError{Code: result.ExitCode}
}

// runInteractiveShell opens an interactive terminal session: stdin and
// stdout must both be real terminals (checked by the caller). The dial
// itself is bounded by g.timeout (the session that follows is not); it
// puts stdin in raw mode, always restored on return regardless of how the
// function returns (a failed restore is reported to stderr instead of
// being dropped), sends the initial terminal size, forwards SIGWINCH as
// resizes (a failed GetSize or Resize is likewise reported, not dropped),
// catches SIGTERM/SIGHUP/SIGQUIT to end the session and restore the
// terminal instead of leaving it raw, and relays until the remote shell
// exits or the user presses Ctrl-] (see shellRelay), printing a short note
// once the terminal is back in cooked mode.
func runInteractiveShell(cmd *cobra.Command, g *globals, c *kvmd.Client) error {
	stdinF := cmd.InOrStdin().(*os.File)
	stdoutF := cmd.OutOrStdout().(*os.File)
	stderr := cmd.ErrOrStderr()

	cols, rows, err := term.GetSize(int(stdoutF.Fd()))
	if err != nil {
		return fmt.Errorf("shell: get terminal size: %w", err)
	}

	dialCtx, dialCancel := context.WithTimeout(context.Background(), g.timeout)
	conn, err := c.Webterm(dialCtx, cols, rows)
	dialCancel()
	if err != nil {
		return err
	}
	defer conn.Close()

	oldState, err := term.MakeRaw(int(stdinF.Fd()))
	if err != nil {
		return fmt.Errorf("shell: enter raw mode: %w", err)
	}
	var escaped bool
	defer func() {
		// This must restore before reporting the escape note (a raw
		// terminal does not reliably move to column 0 on its own), which is
		// why both live in this one deferred closure instead of a defer
		// each: defers run last-registered-first, so a defer registered
		// after this one would run before it, the wrong order.
		if rerr := term.Restore(int(stdinF.Fd()), oldState); rerr != nil {
			fmt.Fprintf(stderr, "glkvm: shell: restore terminal: %v\n", rerr)
		}
		if escaped {
			fmt.Fprintln(stderr, "glkvm: shell: closed (Ctrl-])")
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resize := make(chan [2]int, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sig)

	// sigErrCh carries the terminating signal's error, if any, out of the
	// goroutine below: cancel() alone would only ever surface as a bare
	// context.Canceled from shellRelay, which does not say why.
	sigErrCh := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case s := <-sig:
				switch s {
				case syscall.SIGWINCH:
					w, h, err := term.GetSize(int(stdoutF.Fd()))
					if err != nil {
						fmt.Fprintf(stderr, "glkvm: shell: get terminal size after resize: %v\n", err)
						continue
					}
					select {
					case resize <- [2]int{w, h}:
					case <-ctx.Done():
						return
					}
				default:
					// SIGTERM, SIGHUP or SIGQUIT: end the session instead of
					// leaving the terminal stuck in raw mode, which is what
					// happened before this handler existed.
					sigErrCh <- fmt.Errorf("shell: closed by signal: %v", s)
					cancel()
					return
				}
			}
		}
	}()

	relayErr := shellRelay(ctx, conn, stdinF, stdoutF, resize, stderr)
	if errors.Is(relayErr, errShellEscape) {
		escaped = true
		return nil
	}
	select {
	case sigErr := <-sigErrCh:
		return sigErr
	default:
		return relayErr
	}
}

func newShellCmd(g *globals) *cobra.Command {
	var command string
	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Open an interactive terminal on the KVM, or run one command with -c",
		Long: "Open an interactive terminal on the KVM device over kvmd's webterm, or, " +
			"with -c, run one command and exit with its remote status, like ssh. In text " +
			"mode the process's own exit code is the remote command's status; in json " +
			"mode glkvm always exits 0 when the session itself worked, and the remote " +
			"status is reported as \"exit_code\" in the result.",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVarP(&command, "command", "c", "", "run one command instead of an interactive shell")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}

		if cmd.Flags().Changed("command") {
			if command == "" {
				return usagef("shell -c requires a non-empty command")
			}
			return runShellCCommand(cmd, g, c, command)
		}

		if !isInteractiveTerminal(cmd.InOrStdin(), cmd.OutOrStdout()) {
			return usagef("shell needs an interactive terminal on stdin and stdout; use -c 'command' to run one command instead")
		}
		return runInteractiveShell(cmd, g, c)
	}
	return cmd
}
