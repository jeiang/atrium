package py

import "errors"

type Classed interface {
	error
	PyClass() string
}

func ExceptionClass(err error) string {
	var c Classed
	if errors.As(err, &c) {
		return c.PyClass()
	}
	return "RuntimeError"
}

func ExceptionText(err error) string {
	return ExceptionClass(err) + ": " + err.Error()
}

type Moduled interface {
	PyModule() string
}

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
