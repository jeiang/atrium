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

// UnicodeEncodeError is the exception str.encode raises; the text is str(e).
type UnicodeEncodeError string

func (e UnicodeEncodeError) Error() string { return string(e) }
func (UnicodeEncodeError) PyClass() string { return "UnicodeEncodeError" }

// ASCIIEncode is s.encode("ascii"): the bytes, or the UnicodeEncodeError CPython raises for the first run of
// non-ASCII characters.
func ASCIIEncode(s string) ([]byte, error) {
	ascii := true
	for i := range len(s) {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return []byte(s), nil
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
			return nil, UnicodeEncodeError(fmt.Sprintf("'ascii' codec can't encode characters in position %d-%d: ordinal not in range(128)", i, j))
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
		return nil, UnicodeEncodeError(fmt.Sprintf("'ascii' codec can't encode character '%s' in position %d: ordinal not in range(128)", char, i))
	}
	return []byte(s), nil
}

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

// OSErrorText is str(e) of the OSError Python raises for the condition a Go network error reports: "[Errno N]
// text" with the Linux number and glibc text, "timed out" for a timeout, "[Errno -2] Name or service not known"
// and "[Errno -3] Temporary failure in name resolution" for resolver failures. ok is false for an error that
// has no such counterpart.
func OSErrorText(err error) (text string, ok bool) {
	var dns *net.DNSError
	var errno syscall.Errno
	var timeout interface{ Timeout() bool }
	switch {
	case errors.As(err, &dns) && dns.IsNotFound:
		return "[Errno -2] Name or service not known", true
	case errors.As(err, &dns) && !dns.IsTimeout:
		return "[Errno -3] Temporary failure in name resolution", true
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &timeout) && timeout.Timeout():
		return "timed out", true
	case errors.As(err, &errno):
		n, msg := linuxErrno(errno)
		return fmt.Sprintf("[Errno %d] %s", n, msg), true
	}
	return "", false
}
