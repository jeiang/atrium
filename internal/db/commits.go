package db

import "sync"

var commits = struct {
	sync.Mutex
	listeners map[chan struct{}]struct{}
}{listeners: make(map[chan struct{}]struct{})}

func Commits() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	commits.Lock()
	commits.listeners[ch] = struct{}{}
	commits.Unlock()
	return ch, func() { commits.Lock(); delete(commits.listeners, ch); commits.Unlock() }
}

func committed() {
	commits.Lock()
	defer commits.Unlock()
	for ch := range commits.listeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
