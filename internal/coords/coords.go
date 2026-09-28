// Package coords converts a screen pixel coordinate into the kvmd absolute
// mouse range (-32768 to 32767).
package coords

import (
	"fmt"
	"math"
)

// ToAbsolute converts pixel px on an axis of size px total into the kvmd
// absolute range. It errors unless 0 <= px < size and size > 1.
func ToAbsolute(px, size int) (int, error) {
	if size <= 1 {
		return 0, fmt.Errorf("coords: size %d must be greater than 1", size)
	}
	if px < 0 || px >= size {
		return 0, fmt.Errorf("coords: px %d out of range [0,%d)", px, size)
	}
	scaled := math.Round(float64(px) * 65535 / float64(size-1))
	return int(scaled) - 32768, nil
}
