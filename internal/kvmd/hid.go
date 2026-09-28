package kvmd

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// HIDState is the parsed shape of GET /hid.
type HIDState struct {
	Online         bool
	Connected      bool
	KeyboardOnline bool
	MouseOnline    bool
	MouseAbsolute  bool
}

// hidResponse mirrors the fields of GET /hid this client uses.
type hidResponse struct {
	Online    bool `json:"online"`
	Connected bool `json:"connected"`
	Keyboard  struct {
		Online bool `json:"online"`
	} `json:"keyboard"`
	Mouse struct {
		Online   bool `json:"online"`
		Absolute bool `json:"absolute"`
	} `json:"mouse"`
}

// HID fetches the current keyboard and mouse HID state.
func (c *Client) HID(ctx context.Context) (HIDState, error) {
	var raw hidResponse
	if err := c.getJSON(ctx, "/hid", nil, &raw); err != nil {
		return HIDState{}, err
	}
	return HIDState{
		Online:         raw.Online,
		Connected:      raw.Connected,
		KeyboardOnline: raw.Keyboard.Online,
		MouseOnline:    raw.Mouse.Online,
		MouseAbsolute:  raw.Mouse.Absolute,
	}, nil
}

// SendKey presses and releases key.
func (c *Client) SendKey(ctx context.Context, key string) error {
	q := url.Values{"key": {key}}
	return c.post(ctx, "/hid/events/send_key", q, nil, "", nil)
}

// KeyState presses (down=true) or releases (down=false) key without the
// matching press or release.
func (c *Client) KeyState(ctx context.Context, key string, down bool) error {
	q := url.Values{"key": {key}, "state": {strconv.FormatBool(down)}}
	return c.post(ctx, "/hid/events/send_key", q, nil, "", nil)
}

// SendShortcut presses keys in order, then releases them in reverse order.
func (c *Client) SendShortcut(ctx context.Context, keys []string) error {
	q := url.Values{"keys": {strings.Join(keys, ",")}}
	return c.post(ctx, "/hid/events/send_shortcut", q, nil, "", nil)
}

// Print types text as raw key events. An empty keymap defaults to "en-us".
// The device's own text length limit is always disabled (limit=0).
func (c *Client) Print(ctx context.Context, text string, slow bool, keymap string) error {
	if keymap == "" {
		keymap = "en-us"
	}
	q := url.Values{
		"limit":  {"0"},
		"keymap": {keymap},
		"slow":   {strconv.FormatBool(slow)},
	}
	return c.post(ctx, "/hid/print", q, strings.NewReader(text), "", nil)
}

// MouseMove moves the mouse to the absolute position (x, y), each in the
// kvmd absolute range -32768 to 32767.
func (c *Client) MouseMove(ctx context.Context, x, y int) error {
	q := url.Values{
		"to_x": {strconv.Itoa(x)},
		"to_y": {strconv.Itoa(y)},
	}
	return c.post(ctx, "/hid/events/send_mouse_move", q, nil, "", nil)
}

// MouseButton presses and releases button.
func (c *Client) MouseButton(ctx context.Context, button string) error {
	q := url.Values{"button": {button}}
	return c.post(ctx, "/hid/events/send_mouse_button", q, nil, "", nil)
}

// MouseButtonState presses (down=true) or releases (down=false) button
// without the matching press or release.
func (c *Client) MouseButtonState(ctx context.Context, button string, down bool) error {
	q := url.Values{"button": {button}, "state": {strconv.FormatBool(down)}}
	return c.post(ctx, "/hid/events/send_mouse_button", q, nil, "", nil)
}

// checkMouseDelta rejects a mouse delta outside the device's -127..127 range
// before any request is sent.
func checkMouseDelta(d int) error {
	if d < -127 || d > 127 {
		return fmt.Errorf("kvmd: mouse delta %d out of range [-127,127]", d)
	}
	return nil
}

// MouseWheel scrolls by (dx, dy), each -127 to 127.
func (c *Client) MouseWheel(ctx context.Context, dx, dy int) error {
	if err := checkMouseDelta(dx); err != nil {
		return err
	}
	if err := checkMouseDelta(dy); err != nil {
		return err
	}
	q := url.Values{
		"delta_x": {strconv.Itoa(dx)},
		"delta_y": {strconv.Itoa(dy)},
	}
	return c.post(ctx, "/hid/events/send_mouse_wheel", q, nil, "", nil)
}

// MouseRelative moves the mouse by (dx, dy), each -127 to 127.
func (c *Client) MouseRelative(ctx context.Context, dx, dy int) error {
	if err := checkMouseDelta(dx); err != nil {
		return err
	}
	if err := checkMouseDelta(dy); err != nil {
		return err
	}
	q := url.Values{
		"delta_x": {strconv.Itoa(dx)},
		"delta_y": {strconv.Itoa(dy)},
	}
	return c.post(ctx, "/hid/events/send_mouse_relative", q, nil, "", nil)
}
