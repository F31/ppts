package render

import "testing"

func TestLookCropPadPxMatchesShared(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{0, 8},
		{1, 8},
		{1.15, 9},
		{0.5, 4},
		{0.4, 4},
		{1.5, 12},
		{2, 16},
	}
	for _, tc := range cases {
		if got := LookCropPadPx(tc.in); got != tc.want {
			t.Errorf("LookCropPadPx(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
