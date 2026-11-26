// Package week trata semanas ISO no formato AAAA-WW e dias AAAA-MM-DD, sempre em UTC.
package week

import (
	"fmt"
	"time"
)

// Of devolve a semana ISO de t (em UTC) no formato AAAA-WW.
func Of(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-%02d", y, w)
}

// Day devolve o dia de t (em UTC) no formato AAAA-MM-DD.
func Day(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// Start devolve a segunda-feira 00:00 UTC da semana ISO "AAAA-WW".
// Falha se o texto for inválido ou a semana não existir naquele ano
// (só alguns anos têm semana 53).
func Start(w string) (time.Time, error) {
	var year, num int
	if n, err := fmt.Sscanf(w, "%4d-%2d", &year, &num); err != nil || n != 2 || len(w) != 7 {
		return time.Time{}, fmt.Errorf("semana %q inválida: use AAAA-WW (ex.: 2026-48)", w)
	}
	// A semana 1 é a que contém o dia 4 de janeiro.
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))
	start := monday.AddDate(0, 0, (num-1)*7)
	if num < 1 || Of(start) != w {
		return time.Time{}, fmt.Errorf("semana %q não existe", w)
	}
	return start, nil
}

// Range devolve o primeiro e o último dia (AAAA-MM-DD) da semana.
func Range(w string) (first, last string, err error) {
	start, err := Start(w)
	if err != nil {
		return "", "", err
	}
	return Day(start), Day(start.AddDate(0, 0, 6)), nil
}
