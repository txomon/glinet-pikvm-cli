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
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
)

// nonInteractiveCols and nonInteractiveRows are the fixed terminal size
// requested by shell -c, which has no real terminal to query.
const (
	nonInteractiveCols = 80
	nonInteractiveRows = 24
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

// shellRelay relays bytes between a local terminal (in, out) and conn,
// forwarding size changes received on resize, until either side reaches its
// natural end: conn.ReadOutput returning io.EOF (the remote shell exited)
// or in.Read returning io.EOF (local input closed). It has no dependency on
// a real TTY, so it is testable with plain io.Reader/io.Writer values and a
// resize channel fed by hand.
//
// It returns as soon as either side ends, without waiting for the other: in
// production, once the remote shell exits, the interactive command must
// return promptly even though the goroutine blocked reading local stdin has
// no way to be interrupted (a real terminal's stdin has no read deadline);
// that goroutine is left to exit when the process itself does.
func shellRelay(ctx context.Context, conn webtermConn, in io.Reader, out io.Writer, resize <-chan [2]int) error {
	doneCh := make(chan error, 2)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if _, werr := conn.Write(buf[:n]); werr != nil {
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

	go func() {
		for {
			data, err := conn.ReadOutput(ctx)
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

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sz, ok := <-resize:
				if !ok {
					return
				}
				_ = conn.Resize(sz[0], sz[1])
			}
		}
	}()

	err := <-doneCh
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
				// a split end marker (plus one byte of margin, so a CRLF
				// pair is never split across two flushes).
				keep := len(s.end)
				if len(s.buf) > keep {
					if ferr := s.flush(s.buf[:len(s.buf)-keep]); ferr != nil {
						return false, ferr
					}
					s.buf = s.buf[len(s.buf)-keep:]
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

// buildShellScript wraps cmd in a subshell, so an "exit" inside cmd ends
// only that subshell (leaving $? as cmd's own status) instead of the whole
// remote session, which would otherwise skip the trailing marker/exit
// lines. stty -echo (2>/dev/null, since the fake's plain pipe is not a tty)
// and blank PS1/PS2 keep the remote session's own noise out of the way.
func buildShellScript(start, end, cmd string) string {
	return fmt.Sprintf("stty -echo 2>/dev/null; PS1=; PS2=\necho %s\n( %s )\necho %s$?\nexit\n", start, cmd, end)
}

// runShellCommand runs cmd on the remote shell over conn, writing its
// captured output to out as it arrives, and returns the remote exit status
// alongside that same output collected as a string (for json mode's
// envelope). ctx bounds the whole run.
func runShellCommand(ctx context.Context, conn webtermConn, cmd string, out io.Writer) (shellCommandResult, error) {
	token, err := randomToken()
	if err != nil {
		return shellCommandResult{}, err
	}
	start := "GLKVM_START_" + token
	end := "GLKVM_END_" + token

	if _, err := conn.Write([]byte(buildShellScript(start, end, cmd))); err != nil {
		return shellCommandResult{}, fmt.Errorf("shell: send command: %w", err)
	}

	var captured bytes.Buffer
	scanner := newMarkerScanner(start, end, io.MultiWriter(out, &captured))
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
	if g.output == "json" {
		out = io.Discard
	}

	result, err := runShellCommand(ctx, conn, command, out)
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
// stdout must both be real terminals (checked by the caller). It puts
// stdin in raw mode, always restored on return, sends the initial terminal
// size, forwards SIGWINCH as resizes, and relays until the remote shell
// exits.
func runInteractiveShell(cmd *cobra.Command, c *kvmd.Client) error {
	stdinF := cmd.InOrStdin().(*os.File)
	stdoutF := cmd.OutOrStdout().(*os.File)

	cols, rows, err := term.GetSize(int(stdoutF.Fd()))
	if err != nil {
		return fmt.Errorf("shell: get terminal size: %w", err)
	}

	conn, err := c.Webterm(context.Background(), cols, rows)
	if err != nil {
		return err
	}
	defer conn.Close()

	oldState, err := term.MakeRaw(int(stdinF.Fd()))
	if err != nil {
		return fmt.Errorf("shell: enter raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(stdinF.Fd()), oldState) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resize := make(chan [2]int, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	defer signal.Stop(sig)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sig:
				w, h, err := term.GetSize(int(stdoutF.Fd()))
				if err != nil {
					continue
				}
				select {
				case resize <- [2]int{w, h}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return shellRelay(ctx, conn, stdinF, stdoutF, resize)
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

		if command != "" {
			return runShellCCommand(cmd, g, c, command)
		}

		if !isInteractiveTerminal(cmd.InOrStdin(), cmd.OutOrStdout()) {
			return usagef("shell needs an interactive terminal on stdin and stdout; use -c 'command' to run one command instead")
		}
		return runInteractiveShell(cmd, c)
	}
	return cmd
}
