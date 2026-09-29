package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	f.mu.Unlock()
	f.events <- struct{}{}
	return nil
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
	go func() { done <- shellRelay(ctx, conn, inR, &out, resize) }()

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

func TestShellCommandTimeout(t *testing.T) {
	f := kvmdfake.New(t)
	_, stderr, code := runCLI(t, f, "shell", "--timeout", "1s", "-c", "sleep 30")
	if code != ExitDevice {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}
