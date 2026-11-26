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

func TestStartAndRange(t *testing.T) {
	cases := []struct {
		week, first, last string
	}{
		{"2026-48", "2026-11-23", "2026-11-29"},
		{"2026-01", "2025-12-29", "2026-01-04"},
		{"2026-53", "2026-12-28", "2027-01-03"},
		{"2020-53", "2020-12-28", "2021-01-03"},
	}
	for _, tc := range cases {
		f, l, err := Range(tc.week)
		if err != nil || f != tc.first || l != tc.last {
			t.Errorf("Range(%s) = %s %s %v, want %s %s", tc.week, f, l, err, tc.first, tc.last)
		}
		s, _ := Start(tc.week)
		if Of(s) != tc.week || Of(s.Add(7*24*time.Hour-time.Second)) != tc.week {
			t.Errorf("Start(%s) inconsistente com Of", tc.week)
		}
	}
	for _, bad := range []string{"2025-53", "2026-00", "2026-54", "26-48", "2026-4", "2026_48", "abcd-ef"} {
		if _, err := Start(bad); err == nil {
			t.Errorf("Start(%q) deveria falhar", bad)
		}
	}
}
