package kvmdfake

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"

	"github.com/coder/websocket"
)

// webtermWSPath is where the real device's nginx serves the webterm (ttyd)
// websocket, per the shell brief's verified device facts.
const webtermWSPath = "/extras/webterm/ttyd/ws"

// webtermHandshake mirrors the ttyd client handshake: the first message a
// client sends, as one binary frame.
type webtermHandshake struct {
	AuthToken string `json:"AuthToken"`
	Columns   int    `json:"columns"`
	Rows      int    `json:"rows"`
}

// webtermResize mirrors a '1' resize frame's JSON payload.
type webtermResize struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

// handleWebterm serves the webterm websocket. It rejects a request without
// valid auth headers with a 302 (matching the real nginx's redirect to the
// login page), otherwise upgrades, reads the handshake, sends a title and a
// motd frame, then bridges input/output to a real /bin/sh child (pipes, no
// pty), recording the handshake size and any resizes for tests.
func (f *Server) handleWebterm(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	f.calls = append(f.calls, Call{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: body})
	user, pass := f.User, f.Password
	f.mu.Unlock()

	if r.Header.Get("X-KVMD-User") != user || r.Header.Get("X-KVMD-Passwd") != pass {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{"tty"},
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	ctx := context.Background()
	defer conn.CloseNow()

	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary {
		return
	}
	var hs webtermHandshake
	if err := json.Unmarshal(data, &hs); err != nil {
		return
	}
	f.mu.Lock()
	f.WebtermHandshake = WebtermSize{Columns: hs.Columns, Rows: hs.Rows}
	echo := f.WebtermEcho
	f.mu.Unlock()

	// Title, then a motd line: this fake's stand-in for the real device's
	// ttyd startup banner ("echo -ne ...\007"; cat /etc/motd).
	if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{'1'}, []byte("glkvm-fake")...)); err != nil {
		return
	}
	if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{'0'}, []byte("fake motd\n")...)); err != nil {
		return
	}

	cmd := exec.Command("/bin/sh")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return
	}
	// Kill the child if the connection drops before the shell exits on its
	// own (e.g. a client-side timeout on a still-running command), so a
	// test never leaves an orphaned process running past its own end.
	defer func() { _ = cmd.Process.Kill() }()

	go func() {
		_ = cmd.Wait()
		pw.Close()
	}()

	// reader: websocket input frames -> child stdin, recording resizes.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageBinary || len(data) == 0 {
				continue
			}
			switch data[0] {
			case '0':
				if echo {
					// A real pty echoes input back as output, CRLF
					// converted, before (and regardless of whether) the
					// shell has processed it: this is what let a marker
					// sent as one literal contiguous string leak into the
					// output stream as if it were real command output (the
					// bug this echo mode exists to catch in tests). Errors
					// writing the echo are ignored: a lost echo frame does
					// not stop the session, unlike a lost real frame below.
					echoed := append([]byte{'0'}, bytes.ReplaceAll(data[1:], []byte("\n"), []byte("\r\n"))...)
					_ = conn.Write(ctx, websocket.MessageBinary, echoed)
				}
				if _, err := stdin.Write(data[1:]); err != nil {
					return
				}
			case '1':
				var rs webtermResize
				if err := json.Unmarshal(data[1:], &rs); err == nil {
					f.mu.Lock()
					f.WebtermResizes = append(f.WebtermResizes, WebtermSize{Columns: rs.Columns, Rows: rs.Rows})
					f.mu.Unlock()
				}
			}
			// '2' (pause) and '3' (resume) are not modeled by this fake.
		}
	}()

	// writer: child stdout+stderr -> websocket '0' output frames, until the
	// child exits (pr hits EOF) or the connection breaks.
	buf := make([]byte, 4096)
	for {
		n, rerr := pr.Read(buf)
		if n > 0 {
			frame := append([]byte{'0'}, buf[:n]...)
			if werr := conn.Write(ctx, websocket.MessageBinary, frame); werr != nil {
				break
			}
		}
		if rerr != nil {
			break
		}
	}

	_ = stdin.Close()
	_ = conn.Close(websocket.StatusNormalClosure, "")
	<-readerDone
}
