package py

import "unicode"

func DecimalValue(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + DigitValue(r)
	}
	return n
}

func IsDecimal(r rune) bool { return unicode.Is(unicode.Nd, r) }

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
