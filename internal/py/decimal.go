package py

import "unicode"

// DecimalValue is int() of a string of \d characters: every Unicode decimal digit counts as its value.
func DecimalValue(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + DigitValue(r)
	}
	return n
}

// IsDecimal reports whether r is a \d character (Unicode category Nd).
func IsDecimal(r rune) bool { return unicode.Is(unicode.Nd, r) }

// DigitValue is the value of the decimal digit r, 0 for any other rune.
func DigitValue(r rune) int {
	for _, rg := range unicode.Nd.R16 {
		if lo, hi := rune(rg.Lo), rune(rg.Hi); r >= lo && r <= hi {
			return int(r-lo) % 10
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if lo, hi := rune(rg.Lo), rune(rg.Hi); r >= lo && r <= hi {
			return int(r-lo) % 10
		}
	}
	return 0
}
