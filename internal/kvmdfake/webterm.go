package kvmdfake

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"syscall"

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
	f.webtermHandshake = WebtermSize{Columns: hs.Columns, Rows: hs.Rows}
	echo := f.WebtermEcho
	f.mu.Unlock()

	// Title, then preferences, then a motd line: this fake's stand-in for
	// the real device's ttyd startup banner ("echo -ne ...\007";
	// cat /etc/motd), extended with a '2' frame so a client's frame-type
	// skipping is exercised for preferences too, not just the title.
	if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{'1'}, []byte("glkvm-fake")...)); err != nil {
		return
	}
	if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{'2'}, []byte(`{"fontSize":14}`)...)); err != nil {
		return
	}
	if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{'0'}, onlcr([]byte("fake motd\n"))...)); err != nil {
		return
	}

	cmd := exec.Command("/bin/sh")
	// Run the child in its own process group, so teardown can kill every
	// descendant it may have spawned (a pipeline, a background job), not
	// just this one direct child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	// Kill the child's whole process group if the connection drops before
	// the shell exits on its own (e.g. a client-side timeout on a
	// still-running command), so a test never leaves an orphaned process
	// running past its own end.
	defer func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()

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
					// converted (onlcr, same as real output below), before
					// (and regardless of whether) the shell has processed
					// it: this is what let a marker sent as one literal
					// contiguous string leak into the output stream as if
					// it were real command output (the bug this echo mode
					// exists to catch in tests). Errors writing the echo
					// are ignored: a lost echo frame does not stop the
					// session, unlike a lost real frame below.
					echoed := append([]byte{'0'}, onlcr(data[1:])...)
					_ = conn.Write(ctx, websocket.MessageBinary, echoed)
				}
				if _, err := stdin.Write(data[1:]); err != nil {
					return
				}
			case '1':
				var rs webtermResize
				if err := json.Unmarshal(data[1:], &rs); err != nil {
					f.mu.Lock()
					f.webtermResizeErrs = append(f.webtermResizeErrs, err.Error())
					f.mu.Unlock()
					continue
				}
				f.mu.Lock()
				f.webtermResizes = append(f.webtermResizes, WebtermSize{Columns: rs.Columns, Rows: rs.Rows})
				f.mu.Unlock()
			}
			// '2' (pause) and '3' (resume) are not modeled by this fake.
		}
	}()

	// writer: child stdout+stderr -> websocket '0' output frames, CRLF
	// converted (onlcr) like a real pty's output processing, until the
	// child exits (pr hits EOF) or the connection breaks.
	buf := make([]byte, 4096)
	for {
		n, rerr := pr.Read(buf)
		if n > 0 {
			frame := append([]byte{'0'}, onlcr(buf[:n])...)
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

// onlcr converts every \n in b to \r\n, matching a real pty's output
// processing (the ONLCR termios flag, on by default), which applies to a
// child's own output and not just to echoed input.
func onlcr(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
}
