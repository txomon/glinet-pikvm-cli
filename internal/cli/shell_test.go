package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

// feedAll feeds chunks into s one at a time and returns the phase after the
// last chunk: whether Feed reported done, and any error.
func feedAll(t *testing.T, s *markerScanner, chunks ...string) bool {
	t.Helper()
	for i, c := range chunks {
		done, err := s.Feed([]byte(c))
		if err != nil {
			t.Fatalf("Feed(%d, %q): %v", i, c, err)
		}
		if done {
			return true
		}
	}
	return false
}

func TestMarkerScannerBasic(t *testing.T) {
	var out bytes.Buffer
	s := newMarkerScanner("START123", "END123", &out)

	noise := "\x1b]0;title\x07some motd\nprompt$ "
	done := feedAll(t, s, noise+"START123\nhello\nworld\nEND123", "0\n")
	if !done {
		t.Fatal("not done")
	}
	if out.String() != "hello\nworld\n" {
		t.Fatalf("out = %q", out.String())
	}
	if s.exitCode != 0 {
		t.Fatalf("exitCode = %d", s.exitCode)
	}
}

func TestMarkerScannerNonZeroExit(t *testing.T) {
	var out bytes.Buffer
	s := newMarkerScanner("ST", "EN", &out)
	done := feedAll(t, s, "ST\nhi\nEN3\n")
	if !done {
		t.Fatal("not done")
	}
	if out.String() != "hi\n" {
		t.Fatalf("out = %q", out.String())
	}
	if s.exitCode != 3 {
		t.Fatalf("exitCode = %d", s.exitCode)
	}
}

// TestMarkerScannerSplitAcrossFrames feeds the start marker, the end marker,
// and the exit status line each split across two separate Feed calls, as
// they would arrive split across two websocket frames.
func TestMarkerScannerSplitAcrossFrames(t *testing.T) {
	var out bytes.Buffer
	s := newMarkerScanner("STARTMARK", "ENDMARK", &out)
	done := feedAll(t, s,
		"noise before STAR", "TMARK\nhello ", "wor", "ld\nEND", "MARK", "42\n",
	)
	if !done {
		t.Fatal("not done")
	}
	if out.String() != "hello world\n" {
		t.Fatalf("out = %q", out.String())
	}
	if s.exitCode != 42 {
		t.Fatalf("exitCode = %d", s.exitCode)
	}
}

// TestMarkerScannerCRLFNormalized checks that CRLF line endings between the
// markers (as a real pty would produce) come out as plain LF.
func TestMarkerScannerCRLFNormalized(t *testing.T) {
	var out bytes.Buffer
	s := newMarkerScanner("ST", "EN", &out)
	done := feedAll(t, s, "ST\r\nline one\r\nline two\r\nEN0\r\n")
	if !done {
		t.Fatal("not done")
	}
	if out.String() != "line one\nline two\n" {
		t.Fatalf("out = %q", out.String())
	}
}

// TestMarkerScannerCRLFSplitAtBoundary checks CRLF normalization still works
// when the \r and \n of a pair land in separate Feed calls right at the
// point where the scanner would otherwise flush.
func TestMarkerScannerCRLFSplitAtBoundary(t *testing.T) {
	var out bytes.Buffer
	s := newMarkerScanner("ST", "EN", &out)
	done := feedAll(t, s, "ST\nabc\r", "\ndef\r\nEN0\n")
	if !done {
		t.Fatal("not done")
	}
	if out.String() != "abc\ndef\n" {
		t.Fatalf("out = %q", out.String())
	}
}

// TestMarkerScannerCRLFStableAcrossChunkSizes feeds the same CRLF stream at
// every possible chunk size and asserts identical normalized output every
// time. This pins the fix for a live bug: a flush cut landing exactly
// between a '\r' and its '\n' let the pair leak through unconverted,
// depending entirely on how the byte stream happened to be chopped into
// frames. Trying every chunk size from 1 up to the whole stream length
// means the boundary lands on every possible byte position at least once.
func TestMarkerScannerCRLFStableAcrossChunkSizes(t *testing.T) {
	const full = "ST\nfile1\r\nfile2\r\nfile3\r\nEN0\n"
	const want = "file1\nfile2\nfile3\n"

	for size := 1; size <= len(full); size++ {
		var out bytes.Buffer
		s := newMarkerScanner("ST", "EN", &out)
		done := false
		for i := 0; i < len(full); i += size {
			end := i + size
			if end > len(full) {
				end = len(full)
			}
			d, err := s.Feed([]byte(full[i:end]))
			if err != nil {
				t.Fatalf("chunk size %d: Feed: %v", size, err)
			}
			if d {
				done = true
				break
			}
		}
		if !done {
			t.Fatalf("chunk size %d: not done", size)
		}
		if out.String() != want {
			t.Fatalf("chunk size %d: out = %q, want %q", size, out.String(), want)
		}
	}
}

func TestMarkerScannerBadExitStatus(t *testing.T) {
	var out bytes.Buffer
	s := newMarkerScanner("ST", "EN", &out)
	_, err := s.Feed([]byte("ST\nhi\nENabc\n"))
	if err == nil {
		t.Fatal("want error")
	}
}

// fakeWebtermConn is an in-memory stand-in for *kvmd.WebtermConn, letting
// shellRelay be tested without a real websocket or terminal.
type fakeWebtermConn struct {
	mu      sync.Mutex
	writes  []byte
	resizes [][2]int
	// resizeErr, if set, is returned by every Resize call instead of nil,
	// for testing that a failed resize is reported rather than dropped.
	resizeErr error
	// events is signaled once per Write and once per Resize call, so a test
	// can wait for both to be recorded before triggering the fake's end
	// (closing outCh), instead of racing them against shellRelay's return.
	events chan struct{}
	outCh  chan []byte
}

func (f *fakeWebtermConn) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.writes = append(f.writes, p...)
	f.mu.Unlock()
	f.events <- struct{}{}
	return len(p), nil
}

func (f *fakeWebtermConn) Resize(cols, rows int) error {
	f.mu.Lock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	err := f.resizeErr
	f.mu.Unlock()
	f.events <- struct{}{}
	return err
}

func (f *fakeWebtermConn) ReadOutput(ctx context.Context) ([]byte, error) {
	select {
	case b, ok := <-f.outCh:
		if !ok {
			return nil, io.EOF
		}
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeWebtermConn) Close() error { return nil }

func TestShellRelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inR, inW := io.Pipe()
	defer inW.Close()

	var out bytes.Buffer
	conn := &fakeWebtermConn{outCh: make(chan []byte, 1), events: make(chan struct{}, 2)}
	resize := make(chan [2]int, 1)
	resize <- [2]int{80, 24}

	go func() { _, _ = inW.Write([]byte("hello")) }()

	done := make(chan error, 1)
	go func() { done <- shellRelay(ctx, conn, inR, &out, resize, io.Discard) }()

	// Wait until the input write and the resize have both been recorded
	// before ending the session, so their effects are guaranteed visible
	// once shellRelay returns.
	<-conn.events
	<-conn.events

	conn.outCh <- []byte("world")
	close(conn.outCh)

	if err := <-done; err != nil {
		t.Fatalf("shellRelay: %v", err)
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if string(conn.writes) != "hello" {
		t.Fatalf("writes = %q", conn.writes)
	}
	if out.String() != "world" {
		t.Fatalf("out = %q", out.String())
	}
	if len(conn.resizes) != 1 || conn.resizes[0] != [2]int{80, 24} {
		t.Fatalf("resizes = %v", conn.resizes)
	}
}

// TestShellRelayCtrlCloseByte checks that byte 0x1d (Ctrl-]) in the local
// input ends the session with errShellEscape, forwarding anything read
// before it in the same chunk, and forwarding nothing after it.
func TestShellRelayCtrlCloseByte(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inR, inW := io.Pipe()
	defer inW.Close()

	var out bytes.Buffer
	conn := &fakeWebtermConn{outCh: make(chan []byte, 1), events: make(chan struct{}, 1)}
	resize := make(chan [2]int)

	go func() { _, _ = inW.Write([]byte("hi\x1dnever sent")) }()

	err := shellRelay(ctx, conn, inR, &out, resize, io.Discard)
	if !errors.Is(err, errShellEscape) {
		t.Fatalf("err = %v, want errShellEscape", err)
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if string(conn.writes) != "hi" {
		t.Fatalf("writes = %q, want %q", conn.writes, "hi")
	}
}

// TestShellRelayReportsResizeFailure checks that a failed Resize call is
// reported to warn instead of being silently dropped, and does not end the
// session.
func TestShellRelayReportsResizeFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inR, inW := io.Pipe()
	defer inW.Close()

	var out, warn bytes.Buffer
	conn := &fakeWebtermConn{
		outCh:     make(chan []byte, 1),
		events:    make(chan struct{}, 1),
		resizeErr: errors.New("boom"),
	}
	resize := make(chan [2]int, 1)
	resize <- [2]int{80, 24}

	done := make(chan error, 1)
	go func() { done <- shellRelay(ctx, conn, inR, &out, resize, &warn) }()

	<-conn.events // the failed Resize call itself was still made

	close(conn.outCh)
	if err := <-done; err != nil {
		t.Fatalf("shellRelay: %v", err)
	}

	if !strings.Contains(warn.String(), "boom") {
		t.Fatalf("warn = %q, want it to mention the resize failure", warn.String())
	}
}

func TestShellCommandTextModePropagatesStatus(t *testing.T) {
	f := kvmdfake.New(t)
	out, stderr, code := runCLI(t, f, "shell", "-c", "echo hi; exit 3")
	if code != 3 {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	if out != "hi\n" {
		t.Fatalf("out = %q", out)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty (a nonzero remote status is not itself an error)", stderr)
	}
}

func TestShellCommandJSONMode(t *testing.T) {
	f := kvmdfake.New(t)
	out, stderr, code := runCLI(t, f, "shell", "-o", "json", "-c", "echo hi; exit 3")
	if code != 0 {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			ExitCode int    `json:"exit_code"`
			Output   string `json:"output"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if !env.OK || env.Result.ExitCode != 3 || env.Result.Output != "hi\n" {
		t.Fatalf("env = %+v", env)
	}
}

func TestShellNoTerminalNoCommandIsUsageError(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "shell")
	if code != ExitUsage || !strings.Contains(stderr, "-c") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

// TestShellEmptyCommandIsUsageError checks that -c ” (the flag explicitly
// given an empty value, as opposed to not given at all) is its own usage
// error, rather than silently falling through to the "needs a terminal"
// message: cmd.Flags().Changed distinguishes the two, since both otherwise
// leave the command variable equal to "".
func TestShellEmptyCommandIsUsageError(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "shell", "-c", "")
	if code != ExitUsage || !strings.Contains(stderr, "empty command") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestShellCommandTimeout(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "shell", "--timeout", "1s", "-c", "sleep 30")
	if code != ExitDevice {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "did not finish within 1s") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// TestShellCommandTimeoutJSONEnvelope is TestShellCommandTimeout's json-mode
// counterpart: the timeout is a device error, reported through the standard
// {"ok":false,"error":{...}} envelope on stdout, with stderr left empty
// (json errors go to stdout, matching where a successful json result goes).
func TestShellCommandTimeoutJSONEnvelope(t *testing.T) {
	f := kvmdfake.New(t)
	out, stderr, code := runCLI(t, f, "shell", "-o", "json", "--timeout", "1s", "-c", "sleep 30")
	if code != ExitDevice {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if env.OK {
		t.Fatalf("env.OK = true, want false")
	}
	if env.Error.Kind != "device" || !strings.Contains(env.Error.Message, "did not finish within 1s") {
		t.Fatalf("env.Error = %+v", env.Error)
	}
}

// TestShellCommandReadingStdinDoesNotConsumeMarkers is a live-confirmed
// regression: a command that reads stdin (e.g. "read x") used to read
// glkvm's own queued marker/exit lines as if they were its input, hanging
// the run instead of completing. The subshell is now redirected from
// /dev/null, so "read x" gets immediate EOF instead.
func TestShellCommandReadingStdinDoesNotConsumeMarkers(t *testing.T) {
	f := kvmdfake.New(t)
	out, stderr, code := runCLI(t, f, "shell", "--timeout", "5s", "-c", "read x; echo got:$x")
	if code != 0 {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	if out != "got:\n" {
		t.Fatalf("out = %q", out)
	}
}

// TestShellCommandTrailingComment checks a command ending in a "#" comment
// does not swallow the closing ")" that used to follow it on the same
// line, which would otherwise leave the subshell unclosed and hang the run
// waiting for the marker/exit lines that can never arrive as intended.
func TestShellCommandTrailingComment(t *testing.T) {
	f := kvmdfake.New(t)
	out, stderr, code := runCLI(t, f, "shell", "--timeout", "5s", "-c", "echo hi # a comment")
	if code != 0 {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	if out != "hi\n" {
		t.Fatalf("out = %q", out)
	}
}

// TestShellCommandTrailingBackslash checks a command ending in an unescaped
// backslash (a line continuation) joins the following line correctly
// rather than corrupting the subshell's closing syntax.
func TestShellCommandTrailingBackslash(t *testing.T) {
	f := kvmdfake.New(t)
	out, stderr, code := runCLI(t, f, "shell", "--timeout", "5s", "-c", "echo hi\\")
	if code != 0 {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	if out != "hi\n" {
		t.Fatalf("out = %q", out)
	}
}

// TestBuildShellScriptPreamble asserts the sent preamble text directly:
// live-confirmed necessary on the real device (an interactive bash on an
// 80x24 pty), but not meaningfully testable end to end through the fake's
// plain /bin/sh, which has none of bash's history expansion or pager
// behavior to reproduce.
func TestBuildShellScriptPreamble(t *testing.T) {
	script := buildShellScript("nonce123", "echo hi")
	for _, want := range []string{
		"stty -echo 2>/dev/null",
		`[ -n "$BASH_VERSION" ] && set +H`,
		"unset HISTFILE PROMPT_COMMAND",
		"export PAGER=cat SYSTEMD_PAGER=cat TERM=dumb",
		"PS1=; PS2=",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q: %q", want, script)
		}
	}
}

// TestShellCommandRequestsWideTerminal checks -c requests a wide pty
// instead of the plain 80x24 the real device otherwise defaults to, which
// live-confirmed wraps long output lines.
func TestShellCommandRequestsWideTerminal(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "shell", "-c", "echo hi")
	if code != 0 {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	if got := f.WebtermHandshakeSize(); got != (kvmdfake.WebtermSize{Columns: nonInteractiveCols, Rows: nonInteractiveRows}) {
		t.Fatalf("handshake = %+v", got)
	}
}

// TestShellCommandExclamationInQuotes exercises the exact live-reported
// input ("a!b" inside double quotes triggers bash history expansion,
// "event not found", when history expansion is left on). The fake's plain
// /bin/sh has no history expansion to reproduce, so this only proves
// glkvm's own script construction does not itself mangle such input; the
// preamble's "set +H" (asserted directly above) is what fixes the real bug
// on the actual device.
func TestShellCommandExclamationInQuotes(t *testing.T) {
	f := kvmdfake.New(t)
	out, stderr, code := runCLI(t, f, "shell", "-c", `echo "a!b"`)
	if code != 0 {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	if out != "a!b\n" {
		t.Fatalf("out = %q", out)
	}
}

// TestShellCommandRealPTYEchoRegression pins the fix for a bug found on a
// live run: the real device's webterm is a real pty, which echoes back
// whatever glkvm sends before "stty -echo" can take effect. The old marker
// scheme sent the marker text itself ("echo GLKVM_START_xxx"), so that
// echoed input line contained a complete marker; the scanner (which cannot
// tell echoed input from real command output) matched it there, capturing
// the echoed command line as if it were output, and reading the literal,
// unexpanded "$?" out of the echoed end-marker line as the exit status,
// which failed to parse. kvmdfake's default WebtermEcho (on) reproduces
// that echo; this test would have failed on the old scheme with exactly
// the reported symptom ("parse remote exit status \"$?\": ... invalid
// syntax") and now exercises the fix (markers built at runtime from
// space-separated pieces, so they never appear literally in sent input).
func TestShellCommandRealPTYEchoRegression(t *testing.T) {
	f := kvmdfake.New(t)
	f.SetWebtermEcho(true) // explicit: this is also the default

	out, stderr, code := runCLI(t, f, "shell", "-c", "echo one; echo two; exit 7")
	if code != 7 {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	if out != "one\ntwo\n" {
		t.Fatalf("out = %q", out)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

// TestShellCommandEchoDisabled exercises the SetWebtermEcho(false) knob:
// output is exactly the same either way, since the marker scheme no longer
// depends on echo being off.
func TestShellCommandEchoDisabled(t *testing.T) {
	f := kvmdfake.New(t)
	f.SetWebtermEcho(false)

	out, stderr, code := runCLI(t, f, "shell", "-c", "echo one; echo two; exit 7")
	if code != 7 {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	if out != "one\ntwo\n" {
		t.Fatalf("out = %q", out)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

// TestShellAuthFailureIsDeviceError checks shell -c's own auth failure
// path (a full CLI invocation, not just the kvmd.Client-level dial): a
// device error (exit 1) naming the reason, not a generic wrapped error.
func TestShellAuthFailureIsDeviceError(t *testing.T) {
	f := kvmdfake.New(t)
	// f.Device() (used by runCLI) hands out ClientUser/ClientPassword; the
	// fake checks incoming requests against User/Password instead, so
	// diverging Password here makes every request, including this one,
	// look like it carries the wrong credentials, without touching what
	// runCLI's config actually sends.
	f.Password = "not-what-the-client-sends"

	out, stderr, code := runCLI(t, f, "shell", "-c", "echo hi")
	if code != ExitDevice {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	if !strings.Contains(stderr, "webterm rejected the credentials") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// TestShellOverTLS exercises wss:// and InsecureTLS end to end: arwen's
// nginx is self-signed, so a real dial needs both the scheme translation
// (Client.wsURL) and the transport's InsecureSkipVerify to actually work.
func TestShellOverTLS(t *testing.T) {
	f := kvmdfake.NewTLS(t)
	out, stderr, code := runCLI(t, f, "shell", "-c", "echo hi")
	if code != 0 {
		t.Fatalf("code %d out %q stderr %q", code, out, stderr)
	}
	if out != "hi\n" {
		t.Fatalf("out = %q", out)
	}
}
