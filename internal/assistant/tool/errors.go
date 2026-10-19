package tool

import "errors"

// Error is ToolError: its text is shown to the model as an is_error tool result.
type Error string

func (e Error) Error() string { return string(e) }

// NotConfigured is Python's NotConfigured(ToolError): the integration the tool needs is not set up.
type NotConfigured string

func (e NotConfigured) Error() string { return string(e) }

// Raised stands for a Python exception that is not a ToolError (OverflowError, ValueError, ...): the loop
// answers "<tool> failed: <Class>" and logs it.
type Raised struct{ Class string }

func (e *Raised) Error() string { return e.Class }

// Message is `str(e) or class name` for a ToolError and reports whether err is one.
func Message(err error) (string, bool) {
	var te Error
	if errors.As(err, &te) {
		return orClass(string(te), "ToolError"), true
	}
	var nc NotConfigured
	if errors.As(err, &nc) {
		return orClass(string(nc), "NotConfigured"), true
	}
	return "", false
}

func orClass(text, class string) string {
	if text == "" {
		return class
	}
	return text
}
