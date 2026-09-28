package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/txomon/glinet-pikvm-cli/internal/edid"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
)

// statusResult is the result of glkvm status.
type statusResult struct {
	Model    string     `json:"model"`
	Version  string     `json:"version"`
	ActiveID string     `json:"active_id"`
	Ports    []portLink `json:"ports"`
	Capture  struct {
		Online     bool   `json:"online"`
		Resolution string `json:"resolution"`
	} `json:"capture"`
	HID struct {
		Online    bool `json:"online"`
		Connected bool `json:"connected"`
	} `json:"hid"`
	EDIDMode string `json:"edid_mode"`
	Profile  string `json:"profile"`
}

// edidProfiles are the named profiles doStatus checks the flashed EDID
// against, beyond the chromebook default.
var edidProfiles = []string{"4k", "2k", "1k"}

// matchEDIDProfile compares flashedHex (already lowercase) against
// edid.ChromebookHex and against the device's edid_list preset contents for
// each named profile, and reports the first match, or "custom".
func matchEDIDProfile(flashedHex string, presets []kvmd.EDIDPreset) string {
	if flashedHex == edid.ChromebookHex {
		return "chromebook"
	}

	byKey := make(map[string]string, len(presets))
	for _, p := range presets {
		byKey[p.Key] = strings.ToLower(p.Content)
	}

	for _, name := range edidProfiles {
		key, _, err := edid.PresetKey(name)
		if err != nil {
			continue
		}
		if content, ok := byKey[key]; ok && content == flashedHex {
			return name
		}
	}

	return "custom"
}

// doStatus gathers model/version, port links, capture, HID and flashed EDID
// state into one result. It is a plain function, not a cobra RunE, so other
// callers (like a future batch runner) can use it without cobra.
func doStatus(ctx context.Context, c *kvmd.Client) (statusResult, error) {
	ver, err := c.Version(ctx)
	if err != nil {
		return statusResult{}, err
	}

	sw, err := c.Switch(ctx)
	if err != nil {
		return statusResult{}, err
	}

	st, err := c.Streamer(ctx)
	if err != nil {
		return statusResult{}, err
	}

	hid, err := c.HID(ctx)
	if err != nil {
		return statusResult{}, err
	}

	flashed, err := c.GetEDID(ctx)
	if err != nil {
		return statusResult{}, err
	}

	presets, err := c.EDIDList(ctx)
	if err != nil {
		return statusResult{}, err
	}

	mode, err := edid.Parse(flashed)
	if err != nil {
		return statusResult{}, fmt.Errorf("parse flashed edid: %w", err)
	}

	var result statusResult
	result.Model = ver.Model
	result.Version = ver.Version
	result.ActiveID = sw.ActiveID
	result.Ports = portLinks(sw)
	result.Capture.Online = st.Online
	result.Capture.Resolution = st.RealResolution
	result.HID.Online = hid.Online
	result.HID.Connected = hid.Connected
	result.EDIDMode = mode.String()
	result.Profile = matchEDIDProfile(flashed, presets)
	return result, nil
}

func newStatusCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Show model, port, capture, HID and EDID status",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		result, err := doStatus(cmd.Context(), c)
		if err != nil {
			return err
		}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintf(w, "model: %s (%s)\n", result.Model, result.Version)
			fmt.Fprintf(w, "active port: %s\n", result.ActiveID)
			for _, p := range result.Ports {
				fmt.Fprintf(w, "  port %d %s: hdmi=%v usb=%v\n", p.Port, p.ID, p.HDMI, p.USB)
			}
			fmt.Fprintf(w, "capture: %s (online=%v)\n", result.Capture.Resolution, result.Capture.Online)
			fmt.Fprintf(w, "hid: online=%v connected=%v\n", result.HID.Online, result.HID.Connected)
			fmt.Fprintf(w, "edid: %s (profile=%s)\n", result.EDIDMode, result.Profile)
		})
	}
	return cmd
}
