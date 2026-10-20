package webfetch

type Blocked string

func (e Blocked) Error() string { return string(e) }
func (Blocked) PyClass() string { return "Blocked" }

type FetchError string

func (e FetchError) Error() string { return string(e) }
func (FetchError) PyClass() string { return "FetchError" }
