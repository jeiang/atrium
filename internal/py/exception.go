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

// Moduled is a Classed error whose class is defined outside builtins; PyModule is its dotted module name.
type Moduled interface {
	PyModule() string
}

// UncaughtText is the last line of the traceback Python prints for an uncaught exception,
// traceback.format_exception_only: the class qualified by its module unless it is a builtin, then ": " and the
// message, or the class alone for an empty message.
func UncaughtText(err error) string {
	class := "RuntimeError"
	var c Classed
	if errors.As(err, &c) {
		class = c.PyClass()
		if m, ok := c.(Moduled); ok && m.PyModule() != "" && m.PyModule() != "builtins" {
			class = m.PyModule() + "." + class
		}
	}
	if err.Error() == "" {
		return class
	}
	return class + ": " + err.Error()
}
