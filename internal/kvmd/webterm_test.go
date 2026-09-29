package kvmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/txomon/glinet-pikvm-cli/internal/kvmdfake"
)

// readUntil calls ReadOutput repeatedly, accumulating output, until the
// accumulated bytes contain want or ctx is done.
func readUntil(t *testing.T, ctx context.Context, conn *WebtermConn, want string) string {
	t.Helper()
	var got strings.Builder
	for {
		if strings.Contains(got.String(), want) {
			return got.String()
		}
		data, err := conn.ReadOutput(ctx)
		if err != nil {
			t.Fatalf("ReadOutput: %v (got so far %q)", err, got.String())
		}
		got.Write(data)
	}
}

func TestWebtermAuthFailure(t *testing.T) {
	f := kvmdfake.New(t)
	d := f.Device()
	d.Password = "wrong"
	c := New(d, 5*time.Second)

	_, err := c.Webterm(context.Background(), 80, 24)
	if err == nil || !strings.Contains(err.Error(), "webterm rejected the credentials") {
		t.Fatalf("got %v", err)
	}
}

func TestWebtermHandshakeRecorded(t *testing.T) {
	f := kvmdfake.New(t)
	c := New(f.Device(), 5*time.Second)

	conn, err := c.Webterm(context.Background(), 100, 40)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The fake only sends the motd frame after recording the handshake
	// (in that order, on its own goroutine), so seeing it here guarantees
	// the handshake is already recorded: without this wait, the check below
	// would race the fake's own goroutine, which has not necessarily
	// processed the handshake yet just because our write to the socket
	// returned.
	readUntil(t, ctx, conn, "fake motd")

	if got := f.WebtermHandshakeSize(); got != (kvmdfake.WebtermSize{Columns: 100, Rows: 40}) {
		t.Fatalf("handshake = %+v", got)
	}
}

func TestWebtermEchoRoundTrip(t *testing.T) {
	f := kvmdfake.New(t)
	c := New(f.Device(), 5*time.Second)

	conn, err := c.Webterm(context.Background(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Discard the motd frame before sending anything, then look for our
	// own echoed output further down the stream: title/preference frames
	// (from the motd/title banner) must not break ReadOutput's skipping.
	readUntil(t, ctx, conn, "fake motd")

	// An arithmetic expansion is evaluated only when the shell actually
	// executes the line, never in the raw echo of the input we sent (which
	// contains the literal, unevaluated "$((1+1))"). Waiting for "hello-2"
	// instead of the sent text itself proves the command really ran,
	// instead of the assertion being satisfied by nothing but the fake's
	// own echo of the input.
	if _, err := conn.Write([]byte("echo hello-$((1+1))\n")); err != nil {
		t.Fatal(err)
	}
	out := readUntil(t, ctx, conn, "hello-2")
	if !strings.Contains(out, "hello-2") {
		t.Fatalf("out = %q", out)
	}
}

func TestWebtermResizeRecorded(t *testing.T) {
	f := kvmdfake.New(t)
	c := New(f.Device(), 5*time.Second)

	conn, err := c.Webterm(context.Background(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := conn.Resize(120, 50); err != nil {
		t.Fatal(err)
	}

	// Give the fake's reader goroutine a moment to record the resize: send
	// a command afterward and wait for its output, which cannot arrive
	// until the resize frame ahead of it in the same stream was read.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.Write([]byte("echo after-resize\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, ctx, conn, "after-resize")

	resizes := f.WebtermResizeLog()
	found := false
	for _, sz := range resizes {
		if sz == (kvmdfake.WebtermSize{Columns: 120, Rows: 50}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("resizes = %+v", resizes)
	}
}

// TestWebtermMalformedResizeRecordsError checks that a resize frame whose
// payload does not parse as JSON is recorded as an error the test can see,
// instead of being silently dropped. WebtermConn's Resize always marshals a
// well-formed payload, so producing a malformed one means dialing the raw
// websocket directly, the same way a client (or a bug in one) could send
// any bytes it wants after a '1'.
func TestWebtermMalformedResizeRecordsError(t *testing.T) {
	f := kvmdfake.New(t)
	d := f.Device()

	header := http.Header{}
	header.Set("X-KVMD-User", d.User)
	header.Set("X-KVMD-Passwd", d.Password)
	wsURL := "ws://" + strings.TrimPrefix(d.URL, "http://") + webtermPath

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader:   header,
		Subprotocols: []string{"tty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	hs, err := json.Marshal(webtermHandshake{Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, hs); err != nil {
		t.Fatal(err)
	}

	// A '1' resize frame whose body is not JSON at all.
	if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{'1'}, []byte("not json")...)); err != nil {
		t.Fatal(err)
	}

	// Establish a happens-before against the fake's own reader goroutine,
	// same as the other tests here: it processes frames in order, so
	// seeing this command's output proves the malformed frame ahead of it
	// was already handled.
	if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{'0'}, []byte("echo checkpoint\n")...)); err != nil {
		t.Fatal(err)
	}
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if typ == websocket.MessageBinary && len(data) > 0 && data[0] == '0' && strings.Contains(string(data[1:]), "checkpoint") {
			break
		}
	}

	errs := f.WebtermResizeErrors()
	if len(errs) != 1 {
		t.Fatalf("resize errors = %v, want exactly one", errs)
	}
}

func TestWebtermCloseOnShellExit(t *testing.T) {
	f := kvmdfake.New(t)
	c := New(f.Device(), 5*time.Second)

	conn, err := c.Webterm(context.Background(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, err := conn.ReadOutput(ctx)
		if err != nil {
			if err != io.EOF {
				t.Fatalf("got %v, want io.EOF", err)
			}
			return
		}
	}
}
