package edid

import "fmt"

// Profiles lists the named EDID profiles glkvm accepts on the command line.
var Profiles = []string{"4k", "2k", "1k", "chromebook"}

var presetKeys = map[string]string{
	"4k": "E3840x2160",
	"2k": "E2560x1440",
	"1k": "E1920x1080",
}

// PresetKey resolves a named profile to either a device-side preset key
// (key non-empty, embedded empty) or an embedded EDID hex blob to upload
// directly (key empty, embedded non-empty). It errors for unknown profiles.
func PresetKey(profile string) (key string, embedded string, err error) {
	if profile == "chromebook" {
		return "", ChromebookHex, nil
	}
	if k, ok := presetKeys[profile]; ok {
		return k, "", nil
	}
	return "", "", fmt.Errorf("unknown edid profile %q, want one of %v", profile, Profiles)
}

// ChromebookHex is the EDID for the gsv1127x's native 1200x752@60 mode,
// matching the device preset E1200x752CB byte for byte.
const ChromebookHex = "00ffffffffffff001d891cc28a0e0000081f0103803c22782a2d71af4f44a9270d5054210800d1c095c095008180814081c0010101019d1cb07041f01d2040783a0020542100001c000000ff003839313234370a202020202020000000fc00474c4b564d0a20202020202020000000fd00304b1e721e000a20202020202001e9020327f04b101f051404130312021101230907078301000065030c001000681a00000101304b00023a801871382d40582c450055502100001e8c0ad08a20e02d10103e96005550210000188c0ad090204031200c405500555021000018f03c00d051a0355060883a0055502100001c00000000000000000000000000000000ae"
