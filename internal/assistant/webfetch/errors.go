package webfetch

// Blocked is webfetch.Blocked: the URL, the address it resolves to or a redirect target is refused.
type Blocked string

func (e Blocked) Error() string { return string(e) }
func (Blocked) PyClass() string { return "Blocked" }

// FetchError is webfetch.FetchError: the fetch ran and failed.
type FetchError string

func (e FetchError) Error() string { return string(e) }
func (FetchError) PyClass() string { return "FetchError" }
