// Package kvmdfake is an in-process fake of the kvmd HTTP API, backed by
// real response captures, for tests in this repo and later packages.
package kvmdfake

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/txomon/glinet-pikvm-cli/internal/config"
	"github.com/txomon/glinet-pikvm-cli/internal/keys"
)

//go:embed testdata/*.json
var testdataFS embed.FS

// Call records one HTTP request the fake received.
type Call struct {
	Method string
	Path   string
	Query  url.Values
	Body   []byte
}

// Failure is a canned error response consumed once by the path it is set on.
type Failure struct {
	Status int
	Kind   string
	Msg    string
}

// MSDDrive mirrors the mass-storage drive stanza in msd.json.
type MSDDrive struct {
	CDROM     bool
	Connected bool
	Image     string
	RW        bool
}

// MSDState mirrors the mass-storage state in msd.json. Later tasks wire up
// the /msd endpoints that read and mutate it.
type MSDState struct {
	Enabled bool
	Online  bool
	Busy    bool
	Drive   MSDDrive
}

// Server is an in-process fake kvmd. Construct with New; it starts an
// httptest.Server and registers a cleanup to close it.
type Server struct {
	t   testing.TB
	ts  *httptest.Server
	URL string

	// ClientUser and ClientPassword are what Device() hands out to the
	// client under test. User and Password are what the fake actually
	// checks incoming requests against; a test can point them apart to
	// exercise auth failure.
	ClientUser     string
	ClientPassword string
	User           string
	Password       string

	mu    sync.Mutex
	calls []Call

	ActivePort     int
	VideoLinks     []bool
	USBLinks       []bool
	SourceOnline   bool
	RealResolution string
	Width          int
	Height         int
	CapturedFPS    int
	HDMISignal     bool
	EDID           string
	EDIDPresets    []map[string]any
	SnapshotJPEG   []byte
	MSD            MSDState
	FailNext       map[string]Failure

	switchDoc  map[string]any
	infoDoc    map[string]any
	versionDoc map[string]any
	hidDoc     map[string]any
}

// loadResult reads testdata/<name>.json, expects the standard
// {"ok":true,"result":...} envelope, and returns the result object.
func loadResult(name string) map[string]any {
	b, err := testdataFS.ReadFile("testdata/" + name + ".json")
	if err != nil {
		panic(fmt.Sprintf("kvmdfake: read %s.json: %v", name, err))
	}
	var env struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		panic(fmt.Sprintf("kvmdfake: parse %s.json: %v", name, err))
	}
	return env.Result
}

// loadArray reads testdata/<name>.json as a bare JSON array (no envelope).
func loadArray(name string) []map[string]any {
	b, err := testdataFS.ReadFile("testdata/" + name + ".json")
	if err != nil {
		panic(fmt.Sprintf("kvmdfake: read %s.json: %v", name, err))
	}
	var arr []map[string]any
	if err := json.Unmarshal(b, &arr); err != nil {
		panic(fmt.Sprintf("kvmdfake: parse %s.json: %v", name, err))
	}
	return arr
}

// New starts a fake kvmd server with default state and registers its
// shutdown with t.Cleanup.
func New(t testing.TB) *Server {
	t.Helper()

	edidGet := loadResult("upgrade_get_edid")
	edid, _ := edidGet["edid"].(string)

	streamerDoc := loadResult("streamer")
	streamerObj, _ := streamerDoc["streamer"].(map[string]any)
	sourceObj, _ := streamerObj["source"].(map[string]any)
	hdmiObj, _ := streamerObj["hdmi"].(map[string]any)
	capturedFPS, _ := sourceObj["captured_fps"].(float64)
	hdmiSignal, _ := hdmiObj["signal"].(bool)

	f := &Server{
		t:              t,
		ClientUser:     "admin",
		ClientPassword: "pw",
		User:           "admin",
		Password:       "pw",

		ActivePort:     3,
		VideoLinks:     []bool{true, true, true, true},
		USBLinks:       []bool{false, true, true, true},
		SourceOnline:   true,
		RealResolution: "1200x752@60",
		Width:          1200,
		Height:         752,
		CapturedFPS:    int(capturedFPS),
		HDMISignal:     hdmiSignal,
		EDID:           edid,
		EDIDPresets:    loadArray("upgrade_edid_list"),
		MSD: MSDState{
			Enabled: true,
			Online:  false,
			Busy:    false,
			Drive:   MSDDrive{CDROM: true, Connected: false, Image: "", RW: false},
		},
		FailNext: map[string]Failure{},

		switchDoc:  loadResult("switch"),
		infoDoc:    loadResult("info"),
		versionDoc: loadResult("upgrade_version"),
		hidDoc:     loadResult("hid"),
	}
	f.SnapshotJPEG = generateSnapshot(f.Width, f.Height)

	f.ts = httptest.NewServer(f.newMux())
	f.URL = f.ts.URL
	t.Cleanup(f.ts.Close)
	return f
}

// generateSnapshot builds a minimal valid JPEG at the given size.
func generateSnapshot(width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		panic(fmt.Sprintf("kvmdfake: encode snapshot: %v", err))
	}
	return buf.Bytes()
}

// Device returns a config.Device pointing at the fake, using the client
// credentials.
func (f *Server) Device() config.Device {
	return config.Device{
		Name:     "fake",
		URL:      f.URL,
		User:     f.ClientUser,
		Password: f.ClientPassword,
	}
}

// Calls returns a copy of every request the fake has received so far.
func (f *Server) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

// CurrentEDID returns the fake's current EDID hex, including any change
// made through a POST /upgrade/edid flash.
func (f *Server) CurrentEDID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.EDID
}

// SetSource updates the streamer's capture state. w and h are the reported
// source resolution; a snapshot is only regenerated at this size when both
// are positive, since a JPEG cannot be encoded at zero size and the offline
// case never reads it.
func (f *Server) SetSource(online bool, real string, w, h int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SourceOnline = online
	f.RealResolution = real
	f.Width = w
	f.Height = h
	if w > 0 && h > 0 {
		f.SnapshotJPEG = generateSnapshot(w, h)
	}
}

// newMux builds the routed handler, wrapped with the shared middleware that
// logs calls, enforces auth, and consumes FailNext.
func (f *Server) newMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", f.routeInfo)
	mux.HandleFunc("GET /api/upgrade/version", f.routeVersion)
	mux.HandleFunc("GET /api/switch", f.routeSwitch)
	mux.HandleFunc("POST /api/switch/set_active", f.routeSetActive)
	mux.HandleFunc("POST /api/switch/set_active_next", f.routeSetActiveNext)
	mux.HandleFunc("POST /api/switch/set_active_prev", f.routeSetActivePrev)
	mux.HandleFunc("GET /api/streamer", f.routeStreamer)
	mux.HandleFunc("GET /api/streamer/snapshot", f.routeSnapshot)
	mux.HandleFunc("GET /api/upgrade/get_edid", f.routeGetEDID)
	mux.HandleFunc("GET /api/upgrade/edid_list", f.routeEDIDList)
	mux.HandleFunc("POST /api/upgrade/edid", f.routeFlashEDID)
	mux.HandleFunc("GET /api/hid", f.routeHID)
	mux.HandleFunc("POST /api/hid/events/send_key", f.routeSendKey)
	mux.HandleFunc("POST /api/hid/events/send_shortcut", f.routeSendShortcut)
	mux.HandleFunc("POST /api/hid/print", f.routePrint)
	mux.HandleFunc("POST /api/hid/events/send_mouse_move", f.routeMouseMove)
	mux.HandleFunc("POST /api/hid/events/send_mouse_button", f.routeMouseButton)
	mux.HandleFunc("POST /api/hid/events/send_mouse_wheel", f.routeMouseWheel)
	mux.HandleFunc("POST /api/hid/events/send_mouse_relative", f.routeMouseRelative)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		f.mu.Lock()
		defer f.mu.Unlock()

		f.calls = append(f.calls, Call{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Body:   body,
		})

		if r.Header.Get("X-KVMD-User") != f.User || r.Header.Get("X-KVMD-Passwd") != f.Password {
			fail(w, http.StatusForbidden, "ForbiddenError", "Forbidden")
			return
		}

		if failure, ok := f.FailNext[r.URL.Path]; ok {
			delete(f.FailNext, r.URL.Path)
			fail(w, failure.Status, failure.Kind, failure.Msg)
			return
		}

		mux.ServeHTTP(w, r)
	})
}

// ok writes a {"ok":true,"result":v} envelope.
func ok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": v})
}

// fail writes a {"ok":false,"result":{"error":kind,"error_msg":msg}} envelope
// with the given HTTP status.
func fail(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":     false,
		"result": map[string]any{"error": kind, "error_msg": msg},
	})
}

// The route* methods assume the caller already holds f.mu (the middleware
// in newMux takes it for the whole request).

func (f *Server) routeInfo(w http.ResponseWriter, r *http.Request) {
	ok(w, f.infoDoc)
}

func (f *Server) routeVersion(w http.ResponseWriter, r *http.Request) {
	ok(w, f.versionDoc)
}

func (f *Server) routeSwitch(w http.ResponseWriter, r *http.Request) {
	f.switchDoc["summary"] = map[string]any{
		"active_id":   fmt.Sprintf("1.%d", f.ActivePort+1),
		"active_port": f.ActivePort,
		"synced":      true,
	}
	f.switchDoc["video"] = map[string]any{"links": boolsToAny(f.VideoLinks)}
	f.switchDoc["usb_otg"] = map[string]any{"links": boolsToAny(f.USBLinks)}
	ok(w, f.switchDoc)
}

func (f *Server) routeSetActive(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("port")
	n, valid := parsePort(raw)
	if !valid {
		fail(w, http.StatusBadRequest, "ValidatorError", fmt.Sprintf("invalid port %q", raw))
		return
	}
	f.ActivePort = n
	ok(w, map[string]any{})
}

func (f *Server) routeSetActiveNext(w http.ResponseWriter, r *http.Request) {
	f.ActivePort = (f.ActivePort + 1) % len(f.VideoLinks)
	ok(w, map[string]any{})
}

func (f *Server) routeSetActivePrev(w http.ResponseWriter, r *http.Request) {
	f.ActivePort = (f.ActivePort - 1 + len(f.VideoLinks)) % len(f.VideoLinks)
	ok(w, map[string]any{})
}

func (f *Server) routeStreamer(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{
		"streamer": map[string]any{
			"source": map[string]any{
				"online":          f.SourceOnline,
				"real_resolution": f.RealResolution,
				"resolution": map[string]any{
					"width":  f.Width,
					"height": f.Height,
				},
				"captured_fps": f.CapturedFPS,
			},
			"hdmi": map[string]any{
				"signal": f.HDMISignal,
			},
		},
	})
}

func (f *Server) routeSnapshot(w http.ResponseWriter, r *http.Request) {
	if !f.SourceOnline {
		fail(w, http.StatusServiceUnavailable, "UnavailableError", "Service Unavailable")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write(f.SnapshotJPEG)
}

func (f *Server) routeGetEDID(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{"edid": f.EDID})
}

func (f *Server) routeEDIDList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(f.EDIDPresets)
}

func (f *Server) routeFlashEDID(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		fail(w, http.StatusBadRequest, "ValidatorError", err.Error())
		return
	}
	stripped := stripWhitespace(r.FormValue("edid"))
	if len(stripped) != 256 && len(stripped) != 512 {
		fail(w, http.StatusBadRequest, "ValidatorError", fmt.Sprintf("invalid edid length %d, want 256 or 512 hex chars", len(stripped)))
		return
	}
	f.EDID = strings.ToLower(stripped)
	ok(w, map[string]any{"status": "success", "message": "EDID data has been written and applied"})
}

func (f *Server) routeHID(w http.ResponseWriter, r *http.Request) {
	ok(w, f.hidDoc)
}

func (f *Server) routeSendKey(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if !keys.Names[key] {
		fail(w, http.StatusBadRequest, "ValidatorError", fmt.Sprintf("invalid key %q", key))
		return
	}
	ok(w, map[string]any{})
}

func (f *Server) routeSendShortcut(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("keys")
	for _, k := range strings.Split(raw, ",") {
		if k == "" {
			continue
		}
		if !keys.Names[k] {
			fail(w, http.StatusBadRequest, "ValidatorError", fmt.Sprintf("invalid key %q", k))
			return
		}
	}
	ok(w, map[string]any{})
}

func (f *Server) routePrint(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{})
}

func (f *Server) routeMouseMove(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{})
}

func (f *Server) routeMouseButton(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{})
}

func (f *Server) routeMouseWheel(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{})
}

func (f *Server) routeMouseRelative(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{})
}

// stripWhitespace removes spaces, tabs, and newlines from s.
func stripWhitespace(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// parsePort mirrors device semantics: "1.N" selects host port N (1-based,
// converted here to the 0-based ActivePort); a bare integer K is taken
// literally as the 0-based ActivePort, matching (mis)use of the raw index.
func parsePort(raw string) (int, bool) {
	if dotted, ok := strings.CutPrefix(raw, "1."); ok {
		n, err := strconv.Atoi(dotted)
		if err != nil {
			return 0, false
		}
		return n - 1, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}

func boolsToAny(bs []bool) []any {
	out := make([]any, len(bs))
	for i, b := range bs {
		out[i] = b
	}
	return out
}
