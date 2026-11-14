//go:build linux || darwin

package mediaagent

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

type Config struct {
	Control        Control `toml:"control"`
	Roots          Roots   `toml:"roots"`
	StateDirectory string  `toml:"state_directory"`
	CacheDirectory string  `toml:"cache_directory"`
	Group          string  `toml:"group"`
	VPN            *VPN    `toml:"vpn"`
}

type Control struct {
	Listen       string         `toml:"listen"`
	TokenFile    string         `toml:"token_file"`
	TLSCert      string         `toml:"tls_cert"`
	TLSKey       string         `toml:"tls_key"`
	AllowedHosts []string       `toml:"allowed_hosts"`
	AllowedPeers []netip.Prefix `toml:"allowed_peers"`
}

type Roots struct {
	Movies    string `toml:"movies"`
	TV        string `toml:"tv"`
	Downloads string `toml:"downloads"`
	Import    string `toml:"import"`
}

type VPN struct {
	Listen        string         `toml:"listen"`
	AllowedPeers  []netip.Prefix `toml:"allowed_peers"`
	AllowedHosts  []string       `toml:"allowed_hosts"`
	TLSCert       string         `toml:"tls_cert"`
	TLSKey        string         `toml:"tls_key"`
	AllowLoopback bool           `toml:"allow_loopback"`
}

const DefaultListen = "127.0.0.1:8482"

type ConfigError struct{ err error }

func (e *ConfigError) Error() string { return e.err.Error() }

func (e *ConfigError) Unwrap() error { return e.err }

func configError(err error) error {
	var pathErr *fs.PathError
	if err == nil || errors.As(err, &pathErr) {
		return err
	}
	return &ConfigError{err: err}
}

func (r Roots) named() [][2]string {
	return [][2]string{
		{protocol.RootMovies, r.Movies},
		{protocol.RootTV, r.TV},
		{protocol.RootDownloads, r.Downloads},
		{protocol.RootImport, r.Import},
	}
}

func LoadConfig(path string) (Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return Config{}, configError(fmt.Errorf("read the media agent config: %w", err))
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
		}
		return Config{}, &ConfigError{err: fmt.Errorf("the media agent config has unknown keys: %s", strings.Join(keys, ", "))}
	}
	if c.Control.Listen == "" {
		c.Control.Listen = DefaultListen
	}
	if err := c.validate(); err != nil {
		return Config{}, configError(err)
	}
	return c, nil
}

func (c Config) validate() error {
	addr, err := listenAddr("control.listen", c.Control.Listen)
	if err != nil {
		return err
	}
	if (c.Control.TLSCert == "") != (c.Control.TLSKey == "") {
		return errors.New("control.tls_cert and control.tls_key must be set together")
	}
	if !addr.Addr().IsLoopback() && c.Control.TLSCert == "" {
		return errors.New("control.listen is not a loopback address, so control.tls_cert and control.tls_key are required")
	}
	if err := absolute("control.token_file", c.Control.TokenFile); err != nil {
		return err
	}
	for _, p := range [][2]string{{"control.tls_cert", c.Control.TLSCert}, {"control.tls_key", c.Control.TLSKey}} {
		if p[1] != "" {
			if err := absolute(p[0], p[1]); err != nil {
				return err
			}
		}
	}
	if !addr.Addr().IsLoopback() && len(c.Control.AllowedPeers) == 0 {
		return errors.New("control.allowed_peers is required for non-loopback listeners")
	}
	if len(c.Control.AllowedHosts) == 0 || slices.Contains(c.Control.AllowedHosts, "") {
		return errors.New("control.allowed_hosts must list at least one host and no empty value")
	}
	var paths [][2]string
	for _, r := range c.Roots.named() {
		if err := absolute("roots."+r[0], r[1]); err != nil {
			return err
		}
		paths = append(paths, [2]string{"roots." + r[0], r[1]})
	}
	for i, a := range paths {
		for _, b := range paths[i+1:] {
			if within(a[1], b[1]) || within(b[1], a[1]) {
				return fmt.Errorf("%s and %s overlap", a[0], b[0])
			}
		}
	}
	for _, d := range [][2]string{{"state_directory", c.StateDirectory}, {"cache_directory", c.CacheDirectory}} {
		if err := absolute(d[0], d[1]); err != nil {
			return err
		}
		for _, r := range paths {
			if within(r[1], d[1]) || within(d[1], r[1]) {
				return fmt.Errorf("%s and %s overlap", d[0], r[0])
			}
		}
	}
	if within(c.StateDirectory, c.CacheDirectory) || within(c.CacheDirectory, c.StateDirectory) {
		return errors.New("state_directory and cache_directory overlap")
	}
	all := append(paths, [2]string{"state_directory", c.StateDirectory}, [2]string{"cache_directory", c.CacheDirectory})
	for i, p := range all {
		resolved, err := resolvePath(p[1])
		if err != nil {
			return err
		}
		all[i][1] = resolved
	}
	for i, p := range all {
		for _, q := range all[i+1:] {
			if within(p[1], q[1]) || within(q[1], p[1]) {
				return fmt.Errorf("%s and %s overlap", p[0], q[0])
			}
		}
	}
	if c.Group != "" {
		if _, err := lookupGroup(c.Group); err != nil {
			return err
		}
	}
	if c.VPN != nil {
		return c.VPN.validate()
	}
	return nil
}

func (v VPN) validate() error {
	addr, err := listenAddr("vpn.listen", v.Listen)
	if err != nil {
		return err
	}
	if addr.Addr().IsLoopback() && !v.AllowLoopback {
		return errors.New("vpn.listen is a loopback address; set vpn.allow_loopback to allow it")
	}
	if len(v.AllowedPeers) == 0 {
		return errors.New("vpn.allowed_peers must list at least one peer prefix")
	}
	if slices.ContainsFunc(v.AllowedPeers, func(p netip.Prefix) bool { return p.Bits() == 0 }) {
		return errors.New("vpn.allowed_peers must not contain 0.0.0.0/0 or ::/0")
	}
	if len(v.AllowedHosts) == 0 || slices.Contains(v.AllowedHosts, "") {
		return errors.New("vpn.allowed_hosts must list at least one host and no empty value")
	}
	if (v.TLSCert == "") != (v.TLSKey == "") {
		return errors.New("vpn.tls_cert and vpn.tls_key must be set together")
	}
	if !addr.Addr().IsLoopback() && v.TLSCert == "" {
		return errors.New("vpn.listen is not a loopback address, so vpn.tls_cert and vpn.tls_key are required")
	}
	for _, p := range [][2]string{{"vpn.tls_cert", v.TLSCert}, {"vpn.tls_key", v.TLSKey}} {
		if p[1] != "" {
			if err := absolute(p[0], p[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func listenAddr(key, value string) (netip.AddrPort, error) {
	addr, err := netip.ParseAddrPort(value)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("%s must be an IP address and port", key)
	}
	ip := addr.Addr()
	if ip.Is4In6() {
		return netip.AddrPort{}, fmt.Errorf("%s must use the IPv4 form", key)
	}
	if ip.Zone() != "" && !ip.IsLinkLocalUnicast() {
		return netip.AddrPort{}, fmt.Errorf("%s permits zones only on link-local addresses", key)
	}
	if ip.WithZone("").Unmap().IsUnspecified() {
		return netip.AddrPort{}, fmt.Errorf("%s must not be a wildcard address", key)
	}
	return addr, nil
}

func absolute(key, path string) error {
	if path == "" {
		return fmt.Errorf("%s is required", key)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%s must be a clean absolute path", key)
	}
	return nil
}

func within(parent, child string) bool {
	if parent == child || parent == "/" {
		return true
	}
	return strings.HasPrefix(child, parent+"/")
}

func lookupGroup(name string) (int, error) {
	if gid, err := strconv.Atoi(name); err == nil {
		if gid < 0 {
			return 0, errors.New("group must not be negative")
		}
		return gid, nil
	}
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, fmt.Errorf("group %q is unknown", name)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, fmt.Errorf("group %q has no numeric id", name)
	}
	return gid, nil
}

func readToken(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read control.token_file: %w", err)
	}
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 {
		return nil, errors.New("control.token_file is empty")
	}
	if len(b) < 32 {
		return nil, errors.New("control.token_file must contain at least 32 bytes")
	}
	return b, nil
}

func resolvePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = resolvePath(parent)
	return filepath.Join(resolved, filepath.Base(path)), err
}
