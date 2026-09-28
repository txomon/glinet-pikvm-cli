package kvmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// EDIDPreset is one device-side EDID preset from GET /upgrade/edid_list.
type EDIDPreset struct {
	Key       string
	Label     string
	Content   string
	Hz        int
	IsDefault bool
}

// edidPresetResponse is the shape of one entry in GET /upgrade/edid_list.
type edidPresetResponse struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Content   string `json:"content"`
	Hz        int    `json:"hz"`
	IsDefault bool   `json:"is_default"`
}

// GetEDID fetches the EDID hex currently applied on the device, lowercased.
func (c *Client) GetEDID(ctx context.Context) (string, error) {
	var out struct {
		EDID string `json:"edid"`
	}
	if err := c.getJSON(ctx, "/upgrade/get_edid", nil, &out); err != nil {
		return "", err
	}
	return strings.ToLower(out.EDID), nil
}

// EDIDList fetches the device's built-in EDID presets. The endpoint returns
// a bare JSON array, not the usual {"ok","result"} envelope.
func (c *Client) EDIDList(ctx context.Context) ([]EDIDPreset, error) {
	resp, err := c.getRaw(ctx, "/upgrade/edid_list", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kvmd: read edid list: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Status: resp.StatusCode, Message: truncate(raw, 200)}
	}

	var items []edidPresetResponse
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("kvmd: decode edid list: %w", err)
	}

	out := make([]EDIDPreset, len(items))
	for i, it := range items {
		out[i] = EDIDPreset{
			Key:       it.Key,
			Label:     it.Label,
			Content:   it.Content,
			Hz:        it.Hz,
			IsDefault: it.IsDefault,
		}
	}
	return out, nil
}

// FlashEDID writes hexEDID as the device's active EDID.
func (c *Client) FlashEDID(ctx context.Context, hexEDID string) error {
	form := url.Values{"edid": {hexEDID}}
	body := strings.NewReader(form.Encode())
	return c.post(ctx, "/upgrade/edid", nil, body, "application/x-www-form-urlencoded", nil)
}
