package kvmd

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

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
	// f.WebtermHandshake is already set: without this wait, the check below
	// would race the fake's own goroutine, which has not necessarily
	// processed the handshake yet just because our write to the socket
	// returned.
	readUntil(t, ctx, conn, "fake motd")

	if f.WebtermHandshake != (kvmdfake.WebtermSize{Columns: 100, Rows: 40}) {
		t.Fatalf("handshake = %+v", f.WebtermHandshake)
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

	if _, err := conn.Write([]byte("echo hello-webterm\n")); err != nil {
		t.Fatal(err)
	}
	out := readUntil(t, ctx, conn, "hello-webterm")
	if !strings.Contains(out, "hello-webterm") {
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

	found := false
	for _, sz := range f.WebtermResizes {
		if sz == (kvmdfake.WebtermSize{Columns: 120, Rows: 50}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("resizes = %+v", f.WebtermResizes)
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
