package py

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
)

func ASCIIEncodeError(s string) error {
	ascii := true
	for i := range len(s) {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return nil
	}
	rs := []rune(s)
	for i, r := range rs {
		if r < 0x80 {
			continue
		}
		j := i
		for j+1 < len(rs) && rs[j+1] >= 0x80 {
			j++
		}
		if j > i {
			return &Exception{"UnicodeEncodeError", fmt.Sprintf("'ascii' codec can't encode characters in position %d-%d: ordinal not in range(128)", i, j)}
		}
		var char string
		switch {
		case r <= 0xff:
			char = fmt.Sprintf(`\x%02x`, r)
		case r <= 0xffff:
			char = fmt.Sprintf(`\u%04x`, r)
		default:
			char = fmt.Sprintf(`\U%08x`, r)
		}
		return &Exception{"UnicodeEncodeError", fmt.Sprintf("'ascii' codec can't encode character '%s' in position %d: ordinal not in range(128)", char, i)}
	}
	return nil
}

func ASCIIEncode(s string) ([]byte, error) {
	if err := ASCIIEncodeError(s); err != nil {
		return nil, err
	}
	return []byte(s), nil
}

type OSError struct {
	Kind     string
	Errno    int
	HasErrno bool
	Strerror string
}

func (e *OSError) Error() string {
	if e.HasErrno {
		return fmt.Sprintf("[Errno %d] %s", e.Errno, e.Strerror)
	}
	return e.Strerror
}

func (e *OSError) PyClass() string { return e.Kind }

func linuxErrno(errno syscall.Errno) (int, string) {
	switch errno {
	case syscall.EPIPE:
		return 32, "Broken pipe"
	case syscall.EADDRNOTAVAIL:
		return 99, "Cannot assign requested address"
	case syscall.ENETUNREACH:
		return 101, "Network is unreachable"
	case syscall.ECONNABORTED:
		return 103, "Software caused connection abort"
	case syscall.ECONNRESET:
		return 104, "Connection reset by peer"
	case syscall.ETIMEDOUT:
		return 110, "Connection timed out"
	case syscall.ECONNREFUSED:
		return 111, "Connection refused"
	case syscall.EHOSTUNREACH:
		return 113, "No route to host"
	}
	text := errno.Error()
	if text != "" {
		text = strings.ToUpper(text[:1]) + text[1:]
	}
	return int(errno), text
}

func OSErrorOf(err error) (*OSError, bool) {
	var dns *net.DNSError
	var errno syscall.Errno
	var timeout interface{ Timeout() bool }
	switch {
	case errors.As(err, &dns) && dns.IsNotFound:
		return &OSError{Kind: "gaierror", Errno: -2, HasErrno: true, Strerror: "Name or service not known"}, true
	case errors.As(err, &dns) && !dns.IsTimeout:
		return &OSError{Kind: "gaierror", Errno: -3, HasErrno: true, Strerror: "Temporary failure in name resolution"}, true
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &timeout) && timeout.Timeout():
		return &OSError{Kind: "TimeoutError", Strerror: "timed out"}, true
	case errors.As(err, &errno):
		n, msg := linuxErrno(errno)
		return &OSError{Kind: "OSError", Errno: n, HasErrno: true, Strerror: msg}, true
	}
	return nil, false
}

func OSErrorText(err error) (text string, ok bool) {
	e, ok := OSErrorOf(err)
	if !ok {
		return "", false
	}
	return e.Error(), true
}
