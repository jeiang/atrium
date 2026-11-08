package tool

import "errors"

type Error string

func (e Error) Error() string { return string(e) }

type NotConfigured string

func (e NotConfigured) Error() string { return string(e) }

type Raised struct{ Class string }

func (e *Raised) Error() string { return e.Class }

func Domain[T error, R interface {
	~string
	error
}](err error) error {
	var target T
	if errors.As(err, &target) {
		return R(target.Error())
	}
	return err
}

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
