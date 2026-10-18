package imapclient

import "time"

// idleRestart is the IDLE re-issue interval: Options.IdleRestartInterval, or idleRestartInterval when unset.
//
// This file is part of atrium's local patch and is not in upstream go-imap.
func (options *Options) idleRestart() time.Duration {
	if options.IdleRestartInterval > 0 {
		return options.IdleRestartInterval
	}
	return idleRestartInterval
}
