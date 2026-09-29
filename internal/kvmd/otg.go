package kvmd

import (
	"context"
	"net/url"
)

// OTGFunctions is the parsed shape of GET /system/otg_functions relevant to
// mass storage. On the Comet X, kvmd's selected MSD image is served by the
// USB gadget function mass_storage.0, which GL's controller only links into
// the host-facing USB configuration while start_cdrom is on; start_flash is
// GL's separate partition-sharing mode and unused here. ApplyError is JSON
// null when there is none, decoded here to "".
type OTGFunctions struct {
	StartCDROM bool
	StartFlash bool
	Ready      bool
	Applying   bool
	ApplyError string
}

// otgFunctionsResponse mirrors the fields of GET /system/otg_functions this
// client uses.
type otgFunctionsResponse struct {
	ApplyError *string `json:"apply_error"`
	Applying   bool    `json:"applying"`
	Ready      bool    `json:"ready"`
	StartCDROM bool    `json:"start_cdrom"`
	StartFlash bool    `json:"start_flash"`
}

// OTGFunctions fetches the current OTG gadget function state.
func (c *Client) OTGFunctions(ctx context.Context) (OTGFunctions, error) {
	var raw otgFunctionsResponse
	if err := c.getJSON(ctx, "/system/otg_functions", nil, &raw); err != nil {
		return OTGFunctions{}, err
	}

	applyError := ""
	if raw.ApplyError != nil {
		applyError = *raw.ApplyError
	}

	return OTGFunctions{
		StartCDROM: raw.StartCDROM,
		StartFlash: raw.StartFlash,
		Ready:      raw.Ready,
		Applying:   raw.Applying,
		ApplyError: applyError,
	}, nil
}

// SetOTGStartCDROM turns the start_cdrom OTG function on or off. This
// rebuilds the device's USB gadget, which briefly drops the host's keyboard
// and mouse. The change is asynchronous on the real device: callers should
// poll OTGFunctions afterward for Ready && !Applying, and check ApplyError,
// before relying on the result.
func (c *Client) SetOTGStartCDROM(ctx context.Context, on bool) error {
	q := url.Values{"start_cdrom": {otgBool(on)}}
	return c.post(ctx, "/system/otg_functions", q, nil, "", nil)
}

// otgBool renders a bool as the OTG functions API's true/false query value.
func otgBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
