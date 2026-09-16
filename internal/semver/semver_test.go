package semver

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.3.0", -1}, {"v0.3.0", "0.3.0", 0}, {"0.3.0-dev", "0.3.0", -1},
		{"0.3.0", "0.3.0-dev", 1}, {"0.10.0", "0.9.9", 1}, {"1.0.0", "0.99.0", 1},
		{"0.4.0-rc1", "0.4.0-rc2", -1},
	}
	for _, c := range cases {
		got, err := Compare(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Compare(%s,%s) = %d, %v; want %d", c.a, c.b, got, err, c.want)
		}
	}
	if _, err := Compare("abc", "0.1.0"); err == nil {
		t.Error("se esperaba error con versión no semver")
	}
	if Less("abc", "0.1.0") || !Less("0.1.0", "0.2.0") {
		t.Error("Less")
	}
}
