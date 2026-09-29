package models

import "testing"

func TestDirection_Constants(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		dir  Direction
		want string
	}{
		{"out", DirectionOut, "out"},
		{"in", DirectionIn, "in"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.dir) != tc.want {
				t.Errorf("Direction = %q, want %q", tc.dir, tc.want)
			}
		})
	}
}
