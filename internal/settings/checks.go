package settings

import "strconv"

func number(value string) float64 {
	f, _ := strconv.ParseFloat(value, 64)
	return f
}

func num(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func Above(lo float64) func(string) error {
	return func(value string) error {
		if number(value) <= lo {
			return Invalid("must be greater than " + num(lo))
		}
		return nil
	}
}

func Within(lo, hi float64) func(string) error {
	return func(value string) error {
		if f := number(value); f <= lo || f > hi {
			return Invalid("must be greater than " + num(lo) + " and at most " + num(hi))
		}
		return nil
	}
}

func Between(lo, hi float64) func(string) error {
	return func(value string) error {
		if f := number(value); f < lo || f > hi {
			return Invalid("must be between " + num(lo) + " and " + num(hi))
		}
		return nil
	}
}
