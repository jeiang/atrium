package accounts

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/config"
)

func IMAPHost(a config.AccountSpec) string { return presets[a.Preset].host }

func IMAPEndpoint(host string) (addr, serverName string) {
	if v, ok := config.OS("ATRIUM_IMAP_ADDR"); ok && v != "" {
		if h, _, err := net.SplitHostPort(v); err == nil {
			return v, h
		}
		return v, host
	}
	return net.JoinHostPort(host, strconv.Itoa(config.IMAPPort)), host
}

func SMTPHost(a config.AccountSpec) string { return presets[a.Preset].smtpHost }

func UncertainDest(a config.AccountSpec) string { return presets[a.Preset].uncertainDest }

type preset struct{ host, smtpHost, uncertainDest string }

var presets = map[string]preset{
	config.ICloud: {"imap.mail.me.com", "smtp.mail.me.com", "Misc"},
	config.Gmail:  {"imap.gmail.com", "smtp.gmail.com", ""},
}

var accountID = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

func NewAccount(name, label, presetName, login string, enabled bool) (config.AccountSpec, error) {
	_, ok := presets[presetName]
	if !ok {
		return config.AccountSpec{}, fmt.Errorf("preset must be %s or %s", config.ICloud, config.Gmail)
	}
	if len(name) > 32 || !accountID.MatchString(name) {
		return config.AccountSpec{}, errors.New("id must be up to 32 lowercase letters, digits and single underscores, starting with a letter")
	}
	if label == "" {
		label = name
	}
	return config.AccountSpec{Name: name, Label: label, Preset: presetName, Login: strings.TrimSpace(login), Enabled: enabled, AuthMethod: config.AuthPassword}, nil
}
