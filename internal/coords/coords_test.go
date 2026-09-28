package coords

import "testing"

func TestToAbsolute(t *testing.T) {
	cases := []struct{ px, size, want int }{
		{0, 1200, -32768},
		{1199, 1200, 32767},
		{600, 1201, 0 - 1},
	}
	for _, c := range cases {
		got, err := ToAbsolute(c.px, c.size)
		if err != nil {
			t.Fatal(err)
		}
		if d := got - c.want; d < -1 || d > 1 {
			t.Fatalf("%d/%d: got %d want %d", c.px, c.size, got, c.want)
		}
	}
	for _, bad := range [][2]int{{-1, 1200}, {1200, 1200}, {0, 0}, {0, 1}} {
		if _, err := ToAbsolute(bad[0], bad[1]); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
}
