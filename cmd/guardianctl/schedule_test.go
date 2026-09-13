package main

import "testing"

func TestCronToOnCalendar(t *testing.T) {
	cases := map[string]string{
		"*/15 * * * *": "*-*-* *:00/15:00",
		"0 3 * * *":    "*-*-* 03:00:00",
		"30 2 * * 1":   "Mon *-*-* 02:30:00",
		"0 */6 * * *":  "*-*-* 00/6:00:00",
		"5 4 1 * *":    "*-*-1 04:05:00",
		"0 9 * * 1-5":  "Mon..Fri *-*-* 09:00:00",
		"0 0 1,15 * *": "*-*-1,15 00:00:00",
		"* * * * *":    "*-*-* *:*:00",
	}
	for in, want := range cases {
		got, err := cronToOnCalendar(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	for _, bad := range []string{"* * * *", "60 * * * *", "* 24 * * *", "0 0 0 * *", "0 0 * * 8"} {
		if _, err := cronToOnCalendar(bad); err == nil {
			t.Errorf("%q: se esperaba error", bad)
		}
	}
}
