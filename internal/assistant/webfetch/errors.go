package webfetch

type Blocked string

func (e Blocked) Error() string { return string(e) }

type FetchError string

func (e FetchError) Error() string { return string(e) }
