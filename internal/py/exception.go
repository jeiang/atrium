package py

import "errors"

// Classed is an error that names the Python exception class it stands for.
type Classed interface {
	error
	PyClass() string
}

// ExceptionText is f"{type(e).__name__}: {e}". An error with no Classed in its chain is a RuntimeError, the
// class the Python app raised for its own failures.
func ExceptionText(err error) string {
	class := "RuntimeError"
	var c Classed
	if errors.As(err, &c) {
		class = c.PyClass()
	}
	return class + ": " + err.Error()
}
