package humanize

import (
	"math"
	"testing"
)

func TestDecimal(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{2, "2"},
		{1.5, "1.5"},
		{1.25, "1.25"},
		{1.254, "1.25"},
		{1.005, "1.01"},
		{1.255, "1.26"},
		{1.125, "1.13"},
		{123.456, "123.46"},
		{0.004, "0"},
		{-0.004, "0"},
		{-1.5, "-1.5"},
		{-12.345, "-12.35"},
		{1234567.891, "1234567.89"},
	}
	for _, c := range cases {
		if got := Decimal(c.in); got != c.want {
			t.Errorf("Decimal(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := Decimal(math.NaN()); got != "NaN" {
		t.Errorf("Decimal(NaN) = %q", got)
	}
}

func TestBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1 KiB"},
		{1536, "1.5 KiB"},
		{1280, "1.25 KiB"},
		{2 << 20, "2 MiB"},
		{1325 << 20, "1.29 GiB"},
		{123456789012, "114.98 GiB"},
		{1<<20 - 1, "1 MiB"},
		{5 << 40, "5 TiB"},
		{-1536, "-1.5 KiB"},
		{-512, "-512 B"},
		{math.MaxInt64, "8 EiB"},
	}
	for _, c := range cases {
		if got := Bytes(c.in); got != c.want {
			t.Errorf("Bytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSizes(t *testing.T) {
	cases := []struct {
		in   []int64
		want []string
	}{
		{nil, []string{}},
		{[]int64{3 << 30, 1 << 30}, []string{"3 GiB", "1 GiB"}},
		{[]int64{5 << 20, 5 << 20}, []string{"5 MiB", "5 MiB"}},
		// Different counts that round alike are written exactly.
		{[]int64{1<<20 - 1, 1 << 20}, []string{"1048575 B", "1048576 B"}},
		{[]int64{2048, 1<<30 + 1, 1 << 30}, []string{"2048 B", "1073741825 B", "1073741824 B"}},
	}
	for _, c := range cases {
		got := Sizes(c.in...)
		if len(got) != len(c.want) {
			t.Fatalf("Sizes(%v) = %q, want %q", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("Sizes(%v) = %q, want %q", c.in, got, c.want)
				break
			}
		}
	}
}
