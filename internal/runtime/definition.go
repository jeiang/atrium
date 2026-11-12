package runtime

import "time"

type Backoff struct {
	Initial    time.Duration
	Maximum    time.Duration
	ResetAfter time.Duration
}

type ServiceDefinition struct {
	Name     string
	Critical bool
	Backoff  Backoff
}
