package kvmd

import (
	"context"
	"fmt"
	"net/url"
)

// SwitchPort is one host port on the switch.
type SwitchPort struct {
	ID      string
	Channel int
	Name    string
}

// SwitchState is the parsed shape of GET /switch.
type SwitchState struct {
	ActiveID   string
	ActivePort int
	Synced     bool
	Ports      []SwitchPort
	VideoLinks []bool
	USBLinks   []bool
}

// switchResponse mirrors the fields of GET /switch this client uses.
type switchResponse struct {
	Summary struct {
		ActiveID   string `json:"active_id"`
		ActivePort int    `json:"active_port"`
		Synced     bool   `json:"synced"`
	} `json:"summary"`
	Model struct {
		Ports []struct {
			ID      string `json:"id"`
			Channel int    `json:"channel"`
			Name    string `json:"name"`
		} `json:"ports"`
	} `json:"model"`
	Video struct {
		Links []bool `json:"links"`
	} `json:"video"`
	USBOTG struct {
		Links []bool `json:"links"`
	} `json:"usb_otg"`
}

// Switch fetches the current switch state.
func (c *Client) Switch(ctx context.Context) (SwitchState, error) {
	var raw switchResponse
	if err := c.getJSON(ctx, "/switch", nil, &raw); err != nil {
		return SwitchState{}, err
	}
	s := SwitchState{
		ActiveID:   raw.Summary.ActiveID,
		ActivePort: raw.Summary.ActivePort,
		Synced:     raw.Summary.Synced,
		VideoLinks: raw.Video.Links,
		USBLinks:   raw.USBOTG.Links,
	}
	for _, p := range raw.Model.Ports {
		s.Ports = append(s.Ports, SwitchPort{ID: p.ID, Channel: p.Channel, Name: p.Name})
	}
	return s, nil
}

// SetActivePort switches to host port n (1 to 4).
func (c *Client) SetActivePort(ctx context.Context, n int) error {
	if n < 1 || n > 4 {
		return fmt.Errorf("port must be 1 to 4, got %d", n)
	}
	q := url.Values{"port": {fmt.Sprintf("1.%d", n)}}
	return c.post(ctx, "/switch/set_active", q, nil, "", nil)
}

// SetActiveNext switches to the next host port.
func (c *Client) SetActiveNext(ctx context.Context) error {
	return c.post(ctx, "/switch/set_active_next", nil, nil, "", nil)
}

// SetActivePrev switches to the previous host port.
func (c *Client) SetActivePrev(ctx context.Context) error {
	return c.post(ctx, "/switch/set_active_prev", nil, nil, "", nil)
}
