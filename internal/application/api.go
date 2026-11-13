package application

import "reflect"

type BodyKind string

const (
	BodyJSON        BodyKind = "json"
	BodyNone        BodyKind = "none"
	BodyMultipart   BodyKind = "multipart"
	BodyBinary      BodyKind = "binary"
	BodyEventStream BodyKind = "event-stream"
)

func (b BodyKind) Valid() bool {
	switch b {
	case BodyJSON, BodyNone, BodyMultipart, BodyBinary, BodyEventStream:
		return true
	}
	return false
}

type APIShape struct {
	Area      Area
	Request   reflect.Type
	Response  reflect.Type
	Success   int
	Body      BodyKind
	Sensitive bool
	Errors    []Code
}
