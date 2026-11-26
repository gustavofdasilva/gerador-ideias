// Package fmtx formata números no padrão brasileiro (1.234 · 1,5 KiB · 12,3%).
package fmtx

import (
	"fmt"
	"strings"
)

// Int agrupa milhares com ponto: 1234567 → "1.234.567".
func Int(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Bytes usa unidades binárias com vírgula decimal: 1536 → "1,5 KiB".
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return strings.Replace(fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp]), ".", ",", 1)
}

// Percent formata uma fração: 0.123 → "12,3%".
func Percent(f float64) string {
	return strings.Replace(fmt.Sprintf("%.1f%%", f*100), ".", ",", 1)
}

// Decimal formata com casas decimais e vírgula: Decimal(2.456, 2) → "2,46".
func Decimal(f float64, places int) string {
	return strings.Replace(fmt.Sprintf("%.*f", places, f), ".", ",", 1)
}
