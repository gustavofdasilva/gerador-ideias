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
