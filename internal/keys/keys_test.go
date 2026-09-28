package keys

import (
	"reflect"
	"testing"
)

func TestParseCombo(t *testing.T) {
	cases := map[string][]string{
		"ctrl+alt+del":   {"ControlLeft", "AltLeft", "Delete"},
		"Super+L":        {"MetaLeft", "KeyL"},
		"shift + f13":    {"ShiftLeft", "F13"},
		"Enter":          {"Enter"},
		"ControlRight+1": {"ControlRight", "Digit1"},
		"esc":            {"Escape"},
		"up":             {"ArrowUp"},
	}
	for in, want := range cases {
		got, err := ParseCombo(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: got %v err %v", in, got, err)
		}
	}
}

func TestParseComboErrors(t *testing.T) {
	for _, in := range []string{"", "ctrl+", "ctrl+nosuchkey", "+a"} {
		if _, err := ParseCombo(in); err == nil {
			t.Fatalf("%q accepted", in)
		}
	}
}

func TestNamesHaveBasics(t *testing.T) {
	for _, k := range []string{"KeyA", "Digit0", "F24", "ControlLeft", "Enter", "Backspace"} {
		if !Names[k] {
			t.Fatalf("missing %s", k)
		}
	}
}
