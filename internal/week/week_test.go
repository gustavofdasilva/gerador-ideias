package week

import (
	"testing"
	"time"
)

func TestOf(t *testing.T) {
	cases := []struct {
		in   time.Time
		want string
	}{
		{time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC), "2026-48"},
		// 1º de janeiro de 2027 é sexta: ainda pertence à semana 53 de 2026.
		{time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "2026-53"},
		// 29/12/2025 é segunda: já é a semana 1 de 2026.
		{time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC), "2026-01"},
		// Domingo 23h em São Paulo já é segunda em UTC.
		{time.Date(2026, 11, 29, 23, 0, 0, 0, time.FixedZone("BRT", -3*3600)), "2026-49"},
	}
	for _, tc := range cases {
		if got := Of(tc.in); got != tc.want {
			t.Errorf("Of(%v) = %s, want %s", tc.in, got, tc.want)
		}
	}
	if got := Day(time.Date(2026, 11, 26, 23, 30, 0, 0, time.FixedZone("BRT", -3*3600))); got != "2026-11-27" {
		t.Errorf("Day = %s", got)
	}
}
