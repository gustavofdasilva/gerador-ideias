package fmtx

import "testing"

func TestFormatting(t *testing.T) {
	cases := []struct{ got, want string }{
		{Int(1234567), "1.234.567"},
		{Int(999), "999"},
		{Int(-1234), "-1.234"},
		{Bytes(1536), "1,5 KiB"},
		{Bytes(200 << 20), "200,0 MiB"},
		{Bytes(10), "10 B"},
		{Percent(0.123), "12,3%"},
		{Decimal(2.456, 2), "2,46"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}
