package settings

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func parseFloat(value string) (float64, error) {
	text := strings.TrimSpace(value)
	body := strings.TrimLeft(text, "+-")
	if strings.HasPrefix(body, "0x") || strings.HasPrefix(body, "0X") {
		return 0, fmt.Errorf("could not convert string to float: %s", strconv.Quote(value))
	}
	return strconv.ParseFloat(text, 64)
}

func number(value string) (float64, error) {
	f, err := parseFloat(value)
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

func NonEmpty(value string) error {
	if strings.TrimSpace(value) == "" {
		return Invalid("must not be empty")
	}
	return nil
}
