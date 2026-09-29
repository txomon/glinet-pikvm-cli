package kvmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// webtermPath is where kvmd's nginx serves the ttyd webterm websocket.
const webtermPath = "/extras/webterm/ttyd/ws"

// webtermHandshake is the first message a ttyd client must send: an
// AuthToken (always empty here, since auth is carried by the upgrade
// request's X-KVMD-User/X-KVMD-Passwd headers instead) and the initial
// terminal size.
type webtermHandshake struct {
	AuthToken string `json:"AuthToken"`
	Columns   int    `json:"columns"`
	Rows      int    `json:"rows"`
}

// webtermResize is a '1' resize frame's JSON payload.
type webtermResize struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

// wsURL builds the webterm websocket URL, translating the device's
// http/https scheme to ws/wss.
func (c *Client) wsURL() string {
	u := c.deviceURL
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u + webtermPath
}

// WebtermConn is one live ttyd terminal session, dialed by Client.Webterm.
type WebtermConn struct {
	conn *websocket.Conn
	// ctx is the connection's own context, independent of whatever context
	// governed the dial: Write, Resize and Close reuse it (Write's
	// signature, matching io.Writer, has no room for one of its own).
	// Reusing the dial's context here instead would be wrong: a caller that
	// bounds the dial with a short timeout (as shell -c does, and as an
	// interactive dial now also does) would find every later Write or
	// Resize failing once that timeout expired, even though the session
	// itself is still alive. cancel cancels ctx; Close calls it, so nothing
	// can block on it forever past a Close.
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	title string
}

// Webterm dials kvmd's webterm (ttyd) websocket and sends its initial
// handshake, requesting a cols x rows terminal. ctx bounds only the dial
// (including sending the handshake); the returned connection gets its own,
// independent context for its Write, Resize and Close (see WebtermConn).
// ReadOutput takes its own context per call, so a caller can read with yet
// another bound.
//
// A non-101 response is an error naming the status; a 302 (nginx's redirect
// to the login page) means the auth headers were rejected.
func (c *Client) Webterm(ctx context.Context, cols, rows int) (*WebtermConn, error) {
	hc := &http.Client{
		Transport: c.transport,
		// Dial's default HTTPClient follows redirects, which would hide a
		// 302 behind whatever the login page itself answers with. Refusing
		// to follow surfaces the 302 as the dial's own response status.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	header := http.Header{}
	header.Set("X-KVMD-User", c.user)
	header.Set("X-KVMD-Passwd", c.password)

	conn, resp, err := websocket.Dial(ctx, c.wsURL(), &websocket.DialOptions{
		HTTPClient:   hc,
		HTTPHeader:   header,
		Subprotocols: []string{"tty"},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusFound {
			return nil, fmt.Errorf("kvmd: webterm rejected the credentials")
		}
		if resp != nil {
			return nil, fmt.Errorf("kvmd: webterm dial: unexpected status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("kvmd: webterm dial: %w", err)
	}

	connCtx, cancel := context.WithCancel(context.Background())
	wc := &WebtermConn{conn: conn, ctx: connCtx, cancel: cancel}

	hs, err := json.Marshal(webtermHandshake{Columns: cols, Rows: rows})
	if err != nil {
		cancel()
		conn.CloseNow()
		return nil, fmt.Errorf("kvmd: webterm build handshake: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, hs); err != nil {
		cancel()
		conn.CloseNow()
		return nil, fmt.Errorf("kvmd: webterm send handshake: %w", err)
	}

	return wc, nil
}

// Write sends p to the remote shell's stdin, as a ttyd input frame ('0' +
// bytes). It implements io.Writer, so WebtermConn can be relayed to directly
// from an interactive terminal's stdin.
func (w *WebtermConn) Write(p []byte) (int, error) {
	frame := make([]byte, 0, len(p)+1)
	frame = append(frame, '0')
	frame = append(frame, p...)
	if err := w.conn.Write(w.ctx, websocket.MessageBinary, frame); err != nil {
		return 0, fmt.Errorf("kvmd: webterm write: %w", err)
	}
	return len(p), nil
}

// Resize sends a ttyd resize frame ('1' + JSON columns/rows).
func (w *WebtermConn) Resize(cols, rows int) error {
	b, err := json.Marshal(webtermResize{Columns: cols, Rows: rows})
	if err != nil {
		return fmt.Errorf("kvmd: webterm build resize: %w", err)
	}
	frame := append([]byte{'1'}, b...)
	if err := w.conn.Write(w.ctx, websocket.MessageBinary, frame); err != nil {
		return fmt.Errorf("kvmd: webterm resize: %w", err)
	}
	return nil
}

// ReadOutput returns the next output payload ('0' frame), skipping title
// ('1') and preference ('2') frames along the way (the title is recorded
// and available through Title). A normal or going-away close (the remote
// shell exiting is a normal close, in this fake and on the real device)
// becomes io.EOF; any other close code, or a transport-level failure, is
// reported as an error instead, since those are not the ordinary "the
// session ended" case.
func (w *WebtermConn) ReadOutput(ctx context.Context) ([]byte, error) {
	for {
		typ, data, err := w.conn.Read(ctx)
		if err != nil {
			switch websocket.CloseStatus(err) {
			case websocket.StatusNormalClosure, websocket.StatusGoingAway:
				return nil, io.EOF
			}
			return nil, fmt.Errorf("kvmd: webterm read: %w", err)
		}
		if typ != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		switch data[0] {
		case '0':
			return data[1:], nil
		case '1':
			w.mu.Lock()
			w.title = string(data[1:])
			w.mu.Unlock()
		}
		// Anything else (a '2' preferences frame, or an unrecognized frame
		// type) is skipped; loop for the next frame.
	}
}

// Title returns the most recent window title frame received, or "" if none
// has arrived yet.
func (w *WebtermConn) Title() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.title
}

// Close closes the connection with a normal-closure status and cancels its
// context, so a Write, Resize or ReadOutput blocked on it does not hang.
func (w *WebtermConn) Close() error {
	defer w.cancel()
	return w.conn.Close(websocket.StatusNormalClosure, "")
}
