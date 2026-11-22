// Package clean makes text from the other side safe to show in a dialog or log line.
package clean

import (
	"regexp"
	"strings"
	"unicode"
)

// IDPattern is the shape of an ask ID. IDs become file names, so nothing else is accepted.
var IDPattern = regexp.MustCompile(`^[a-z0-9]{8,40}$`)

// Text drops control characters (keeping \n; a tab becomes a space) and invisible format
// characters such as bidi overrides, then keeps at most max runes.
func Text(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		switch {
		case r == '\n':
		case r == '\t':
			r = ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == unicode.ReplacementChar:
			continue
		}
		if n == max {
			b.WriteRune('…')
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// Line is Text on a single line.
func Line(s string, max int) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(Text(s, max), "\n", " ")), " ")
}
