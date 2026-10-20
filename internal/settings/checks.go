package settings

import (
	"math"
	"strconv"

	"github.com/jeiang/atrium/internal/py"
)

func number(value string) (float64, error) {
	f, err := py.ParseFloat(value)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, Invalid("must be a number")
	}
	return f, nil
}

func num(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func Above(lo float64) func(string) error {
	return func(value string) error {
		f, err := number(value)
		if err != nil {
			return err
		}
		if f <= lo {
			return Invalid("must be greater than " + num(lo))
		}
		return nil
	}
}

func Within(lo, hi float64) func(string) error {
	return func(value string) error {
		f, err := number(value)
		if err != nil {
			return err
		}
		if f <= lo || f > hi {
			return Invalid("must be greater than " + num(lo) + " and at most " + num(hi))
		}
		return nil
	}
}

func Between(lo, hi float64) func(string) error {
	return func(value string) error {
		f, err := number(value)
		if err != nil {
			return err
		}
		if f < lo || f > hi {
			return Invalid("must be between " + num(lo) + " and " + num(hi))
		}
		return nil
	}
}
