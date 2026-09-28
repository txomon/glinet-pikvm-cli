// Package keys parses key combo strings into kvmd web key names.
package keys

import (
	"fmt"
	"strings"
)

// aliases maps case-insensitive shorthand tokens to kvmd web key names.
var aliases = map[string]string{
	"ctrl":      "ControlLeft",
	"control":   "ControlLeft",
	"shift":     "ShiftLeft",
	"alt":       "AltLeft",
	"altgr":     "AltRight",
	"super":     "MetaLeft",
	"meta":      "MetaLeft",
	"win":       "MetaLeft",
	"cmd":       "MetaLeft",
	"del":       "Delete",
	"delete":    "Delete",
	"esc":       "Escape",
	"escape":    "Escape",
	"enter":     "Enter",
	"return":    "Enter",
	"tab":       "Tab",
	"space":     "Space",
	"backspace": "Backspace",
	"bs":        "Backspace",
	"up":        "ArrowUp",
	"down":      "ArrowDown",
	"left":      "ArrowLeft",
	"right":     "ArrowRight",
	"home":      "Home",
	"end":       "End",
	"pageup":    "PageUp",
	"pagedown":  "PageDown",
	"insert":    "Insert",
}

// resolveToken maps one combo token (already trimmed, non-empty) to a kvmd
// web key name, or returns an error naming the bad token.
func resolveToken(tok string) (string, error) {
	lower := strings.ToLower(tok)

	if name, ok := aliases[lower]; ok {
		return name, nil
	}

	if len(lower) >= 2 && lower[0] == 'f' {
		if n, ok := parseUint(lower[1:]); ok && n >= 1 && n <= 24 {
			return fmt.Sprintf("F%d", n), nil
		}
	}

	if len(tok) == 1 {
		r := tok[0]
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return "Key" + strings.ToUpper(tok), nil
		}
		if r >= '0' && r <= '9' {
			return "Digit" + tok, nil
		}
	}

	if Names[tok] {
		return tok, nil
	}

	return "", fmt.Errorf("keys: unknown key %q", tok)
}

// parseUint parses a non-negative decimal integer with no sign, no
// whitespace, and only digit characters.
func parseUint(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// ParseCombo splits s on '+', trims each part, and resolves it to a kvmd web
// key name. It returns an error naming the first empty or unknown token.
func ParseCombo(s string) ([]string, error) {
	parts := strings.Split(s, "+")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		tok := strings.TrimSpace(part)
		if tok == "" {
			return nil, fmt.Errorf("keys: empty token in combo %q", s)
		}
		name, err := resolveToken(tok)
		if err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, nil
}
