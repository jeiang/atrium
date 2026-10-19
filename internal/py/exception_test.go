package py

import (
	"errors"
	"fmt"
	"testing"
)

type classed string

func (c classed) Error() string { return string(c) }
func (classed) PyClass() string { return "ValueError" }

func TestExceptionClass(t *testing.T) {
	if got := ExceptionClass(errors.New("x")); got != "RuntimeError" {
		t.Errorf("plain error = %q", got)
	}
	wrapped := fmt.Errorf("outer: %w", classed("bad"))
	if got := ExceptionClass(wrapped); got != "ValueError" {
		t.Errorf("wrapped = %q", got)
	}
	if got := ExceptionText(wrapped); got != "ValueError: outer: bad" {
		t.Errorf("text = %q", got)
	}
}
