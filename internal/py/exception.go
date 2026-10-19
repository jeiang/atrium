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
