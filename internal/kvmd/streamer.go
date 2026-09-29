package kvmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

// StreamerState is the parsed shape of GET /streamer's source and hdmi info.
type StreamerState struct {
	Online         bool
	RealResolution string
	Width          int
	Height         int
	CapturedFPS    int
	HDMISignal     bool
}

// realResolutionRe matches the "<W>x<H>@<F>" shape of a settled
// real_resolution, such as "1200x752@60". Anything else (a bare
// "no signal", the device's own "no_signal", an "invalid_resolution:"
// prefixed value, or an empty string) does not match.
var realResolutionRe = regexp.MustCompile(`^([0-9]+)x([0-9]+)@[0-9.]+$`)

// ParsedResolution reports the width and height that RealResolution decodes
// to, when it matches "<W>x<H>@<F>". ok is false when it does not parse.
func (s StreamerState) ParsedResolution() (w, h int, ok bool) {
	m := realResolutionRe.FindStringSubmatch(s.RealResolution)
	if m == nil {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(m[1])
	h, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return w, h, true
}

// Valid reports whether s reflects a usable, settled capture: the source is
// online, the HDMI link has signal, real_resolution parses as "<W>x<H>@<F>",
// and that parsed W,H matches the separately reported Width,Height. A real
// device passes through several inconsistent in-between states (hdmi signal
// down, real_resolution briefly "no_signal", real_resolution updated before
// the reported resolution catches up) while a capture settles; each of
// those is invalid here. Valid returns nil when settled, otherwise an error
// quoting RealResolution and naming which condition failed.
func (s StreamerState) Valid() error {
	if !s.Online {
		return fmt.Errorf("streamer capture is not usable: source offline, real_resolution %q", s.RealResolution)
	}
	if !s.HDMISignal {
		return fmt.Errorf("streamer capture is not usable: hdmi signal down, real_resolution %q", s.RealResolution)
	}
	w, h, ok := s.ParsedResolution()
	if !ok {
		return fmt.Errorf("streamer capture is not usable: real_resolution %q does not parse as WxH@F", s.RealResolution)
	}
	if w != s.Width || h != s.Height {
		return fmt.Errorf("streamer capture is not usable: real_resolution %q does not match reported resolution %dx%d", s.RealResolution, s.Width, s.Height)
	}
	return nil
}

// streamerResponse is the shape of GET /streamer this client uses.
type streamerResponse struct {
	Streamer struct {
		Source struct {
			Online         bool   `json:"online"`
			RealResolution string `json:"real_resolution"`
			Resolution     struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			} `json:"resolution"`
			CapturedFPS int `json:"captured_fps"`
		} `json:"source"`
		HDMI struct {
			Signal bool `json:"signal"`
		} `json:"hdmi"`
	} `json:"streamer"`
}

// Streamer fetches the current streamer capture state.
func (c *Client) Streamer(ctx context.Context) (StreamerState, error) {
	var raw streamerResponse
	if err := c.getJSON(ctx, "/streamer", nil, &raw); err != nil {
		return StreamerState{}, err
	}
	return StreamerState{
		Online:         raw.Streamer.Source.Online,
		RealResolution: raw.Streamer.Source.RealResolution,
		Width:          raw.Streamer.Source.Resolution.Width,
		Height:         raw.Streamer.Source.Resolution.Height,
		CapturedFPS:    raw.Streamer.Source.CapturedFPS,
		HDMISignal:     raw.Streamer.HDMI.Signal,
	}, nil
}

// Snapshot fetches one JPEG frame from the streamer. A non-2xx status or a
// body that is not image/jpeg (the device's JSON error shape when there is
// no signal) becomes an *APIError.
func (c *Client) Snapshot(ctx context.Context) ([]byte, error) {
	resp, err := c.getRaw(ctx, "/streamer/snapshot", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kvmd: read snapshot: %w", err)
	}

	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if ok && resp.Header.Get("Content-Type") == "image/jpeg" {
		return raw, nil
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &APIError{Status: resp.StatusCode, Message: truncate(raw, 200)}
	}
	var ee envelopeError
	if err := json.Unmarshal(env.Result, &ee); err != nil || ee.Error == "" {
		return nil, &APIError{Status: resp.StatusCode, Message: truncate(raw, 200)}
	}
	return nil, &APIError{Status: resp.StatusCode, Kind: ee.Error, Message: ee.ErrorMsg}
}
