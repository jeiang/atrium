package py

import "errors"

// Classed is an error that names the Python exception class it stands for.
type Classed interface {
	error
	PyClass() string
}

// ExceptionClass is type(e).__name__: the class of the first Classed in the chain, RuntimeError (the class the
// Python app raised for its own failures) for an error with none.
func ExceptionClass(err error) string {
	var c Classed
	if errors.As(err, &c) {
		return c.PyClass()
	}
	return "RuntimeError"
}

// ExceptionText is f"{type(e).__name__}: {e}".
func ExceptionText(err error) string {
	return ExceptionClass(err) + ": " + err.Error()
}
