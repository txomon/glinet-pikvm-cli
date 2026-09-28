// Package edid normalizes and decodes EDID hex blobs from the gsv1127x
// capture device and applies its capture rules.
package edid

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

const header = "00ffffffffffff00"

// Mode describes a single detailed timing decoded from an EDID.
type Mode struct {
	Width         int
	Height        int
	RefreshHz     float64
	PixelClockKHz int
}

// FPS rounds RefreshHz to the nearest integer frame rate.
func (m Mode) FPS() int {
	return int(math.Round(m.RefreshHz))
}

// String renders the mode as "WIDTHxHEIGHT@REFRESH".
func (m Mode) String() string {
	return fmt.Sprintf("%dx%d@%.2f", m.Width, m.Height, m.RefreshHz)
}

// Normalize strips whitespace, lowercases, and validates an EDID hex string.
// It requires valid hex of length 256 or 512, the standard 8-byte header,
// and a base block checksum of zero. A 512-char (two block) EDID also has
// its extension block checksum verified.
func Normalize(s string) (string, error) {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		b.WriteRune(r)
	}
	n := strings.ToLower(b.String())

	if len(n) != 256 && len(n) != 512 {
		return "", fmt.Errorf("edid length %d, want 256 or 512 hex chars", len(n))
	}

	raw, err := hex.DecodeString(n)
	if err != nil {
		return "", fmt.Errorf("edid is not valid hex: %w", err)
	}

	if !strings.HasPrefix(n, header) {
		return "", fmt.Errorf("edid header mismatch, want %s", header)
	}

	if sum := checksum(raw[0:128]); sum != 0 {
		return "", fmt.Errorf("base block checksum failed, byte sum mod 256 = %d", sum)
	}

	if len(raw) == 256 {
		if sum := checksum(raw[128:256]); sum != 0 {
			return "", fmt.Errorf("extension block checksum failed, byte sum mod 256 = %d", sum)
		}
	}

	return n, nil
}

func checksum(block []byte) int {
	sum := 0
	for _, b := range block {
		sum += int(b)
	}
	return sum % 256
}

// Parse decodes the first detailed timing descriptor (at byte 54) of an
// EDID hex string into a Mode. hex is not normalized first; callers that
// need validation should call Normalize themselves.
func Parse(hexStr string) (Mode, error) {
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		return Mode{}, fmt.Errorf("edid is not valid hex: %w", err)
	}
	if len(raw) < 54+18 {
		return Mode{}, fmt.Errorf("edid too short for a detailed timing at byte 54")
	}

	o := 54
	clock := int(raw[o]) | int(raw[o+1])<<8
	if clock == 0 {
		return Mode{}, fmt.Errorf("first descriptor is not a detailed timing")
	}

	hActive := int(raw[o+2]) | int(raw[o+4]>>4)<<8
	hBlank := int(raw[o+3]) | int(raw[o+4]&0x0f)<<8
	vActive := int(raw[o+5]) | int(raw[o+7]>>4)<<8
	vBlank := int(raw[o+6]) | int(raw[o+7]&0x0f)<<8

	pixelClockKHz := clock * 10
	refresh := float64(pixelClockKHz*1000) / float64((hActive+hBlank)*(vActive+vBlank))

	return Mode{
		Width:         hActive,
		Height:        vActive,
		RefreshHz:     refresh,
		PixelClockKHz: pixelClockKHz,
	}, nil
}

var supportedFPS = map[int]bool{
	24: true, 25: true, 29: true, 30: true, 31: true,
	49: true, 50: true, 51: true, 59: true, 60: true, 61: true,
	75: true, 76: true, 90: true, 91: true, 119: true, 120: true, 121: true,
}

// CheckCapture reports whether the gsv1127x capture device can capture m.
// It returns nil when the mode satisfies every rule, otherwise an error
// naming the failed rule.
func CheckCapture(m Mode) error {
	if m.Width%4 != 0 || m.Height%4 != 0 {
		return fmt.Errorf("width %d and height %d must both be divisible by 4", m.Width, m.Height)
	}
	if m.Width <= 120 {
		return fmt.Errorf("width %d must be above 120", m.Width)
	}
	if m.Height <= 120 {
		return fmt.Errorf("height %d must be above 120", m.Height)
	}
	fps := m.FPS()
	if !supportedFPS[fps] {
		return fmt.Errorf("fps %d not in supported list", fps)
	}
	return nil
}
