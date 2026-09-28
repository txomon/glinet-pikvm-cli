package kvmd

import "context"

// Version is the parsed shape of GET /upgrade/version.
type Version struct {
	Model   string `json:"model"`
	Version string `json:"version"`
}

// Version fetches the device model and firmware version.
func (c *Client) Version(ctx context.Context) (Version, error) {
	var v Version
	if err := c.getJSON(ctx, "/upgrade/version", nil, &v); err != nil {
		return Version{}, err
	}
	return v, nil
}

// Info fetches the raw result of GET /info (meta, system, extras, health).
func (c *Client) Info(ctx context.Context) (map[string]any, error) {
	var m map[string]any
	if err := c.getJSON(ctx, "/info", nil, &m); err != nil {
		return nil, err
	}
	return m, nil
}
