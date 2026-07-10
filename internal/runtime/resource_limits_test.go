package runtime

import "testing"

func TestParseMemBytes(t *testing.T) {
	ok := map[string]int64{
		"1073741824": 1073741824,
		"512":        512,
		"768m":       768 << 20,
		"768M":       768 << 20,
		"2g":         2 << 30,
		"2G":         2 << 30,
		"512mb":      512 << 20,
		"1gb":        1 << 30,
		"64k":        64 << 10,
		"4kb":        4 << 10,
		"1t":         1 << 40,
		"1024b":      1024,
		"  256m  ":   256 << 20, // surrounding whitespace tolerated
	}
	for in, want := range ok {
		got, valid := parseMemBytes(in)
		if !valid || got != want {
			t.Errorf("parseMemBytes(%q) = (%d, %v), want (%d, true)", in, got, valid, want)
		}
	}

	bad := []string{"", "   ", "abc", "m", "12x", "-5m", "0", "0g", "1.5g", "g2"}
	for _, in := range bad {
		if got, valid := parseMemBytes(in); valid {
			t.Errorf("parseMemBytes(%q) = (%d, true), want invalid", in, got)
		}
	}
}
