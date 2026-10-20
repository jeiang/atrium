package py

import "math"

func Sum(values []float64) float64 {
	var s, c float64
	for _, x := range values {
		s, c = add(s, c, x)
	}
	return finish(s, c)
}

func SumSq(values []float64) float64 {
	var s, c float64
	for _, x := range values {
		sq := float64(x * x)
		s, c = add(s, c, sq)
	}
	return finish(s, c)
}

func add(s, c, x float64) (float64, float64) {
	t := s + x
	if math.Abs(s) >= math.Abs(x) {
		c += (s - t) + x
	} else {
		c += (x - t) + s
	}
	return t, c
}

func finish(s, c float64) float64 {
	if c != 0 && !math.IsInf(c, 0) && !math.IsNaN(c) {
		s += c
	}
	return s
}
