package config

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jeiang/atrium/internal/settings"
)

const HostConfigEnv = "ATRIUM_HOST_CONFIG"

type HostConnection struct {
	Kind            string
	Label           string
	Config          map[string]string
	Transport       map[string]string
	Capabilities    []string
	CredentialFiles map[string]string
}

type Host struct {
	Values      map[string]string
	Connections map[string]HostConnection
}

func (h Host) Value(key string) string { return h.Values[key] }

func (h Host) Connection(id string) (HostConnection, bool) {
	c, ok := h.Connections[id]
	return c, ok
}

func (h Host) LogValue() string {
	return fmt.Sprintf("%d host values, %d connections", len(h.Values), len(h.Connections))
}

func LoadHost(env Env, filename string) (Host, error) {
	defs := settings.HostDefs()
	h := Host{Values: map[string]string{}, Connections: map[string]HostConnection{}}
	for _, d := range defs {
		h.Values[d.Key] = d.Default
	}
	if filename != "" {
		if err := h.readFile(env, defs, filename); err != nil {
			return Host{}, err
		}
	}
	for _, d := range defs {
		if err := h.readEnv(env, d); err != nil {
			return Host{}, err
		}
	}
	h.deriveHostConnections()
	return h, nil
}

func (h *Host) readEnv(env Env, d settings.HostDef) error {
	name := "ATRIUM_" + strings.ToUpper(d.Module) + "_" + strings.ToUpper(d.Key)
	value := strings.TrimSpace(get(env, name))
	if path, set := env(name + "_FILE"); set {
		if value != "" {
			return fmt.Errorf("config: %s and %s_FILE are both set; set only one", name, name)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("config: %s_FILE: %w", name, err)
		}
		value = strings.TrimSpace(string(raw))
	}
	if value == "" {
		return nil
	}
	if err := validateHostValue(d, value); err != nil {
		return fmt.Errorf("config: %s: %w", name, err)
	}
	h.Values[d.Key] = value
	return nil
}

func (h *Host) readFile(env Env, defs []settings.HostDef, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: %s: %w", HostConfigEnv, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("config: %s: %w", path, err)
	}
	byModule := map[string]map[string]settings.HostDef{}
	for _, d := range defs {
		if byModule[d.Module] == nil {
			byModule[d.Module] = map[string]settings.HostDef{}
		}
		byModule[d.Module][d.Key] = d
	}
	for _, module := range slices.Sorted(maps.Keys(doc)) {
		if module == "connections" {
			if err := h.readConnections(path, doc[module]); err != nil {
				return err
			}
			continue
		}
		keys, known := byModule[module]
		table, isTable := doc[module].(map[string]any)
		if !known || !isTable {
			return fmt.Errorf("config: %s: unknown host key %q", path, module)
		}
		for _, name := range slices.Sorted(maps.Keys(table)) {
			d, ok := keys[name]
			if !ok {
				return fmt.Errorf("config: %s: unknown host key %q", path, module+"."+name)
			}
			value, err := hostScalar(env, module+"."+name, table[name])
			if err != nil {
				return err
			}
			if err := validateHostValue(d, value); err != nil {
				return fmt.Errorf("config: %s: %s: %w", path, module+"."+name, err)
			}
			h.Values[d.Key] = value
		}
	}
	return nil
}

func (h *Host) readConnections(path string, raw any) error {
	table, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("config: %s: connections must be a table", path)
	}
	for _, id := range slices.Sorted(maps.Keys(table)) {
		entry, ok := table[id].(map[string]any)
		if !ok {
			return fmt.Errorf("config: %s: connections.%s must be a table", path, id)
		}
		c := HostConnection{Config: map[string]string{}, Transport: map[string]string{}, CredentialFiles: map[string]string{}}
		for _, name := range slices.Sorted(maps.Keys(entry)) {
			switch name {
			case "kind":
				c.Kind = strings.TrimSpace(fmt.Sprint(entry[name]))
			case "label":
				c.Label = strings.TrimSpace(fmt.Sprint(entry[name]))
			case "config":
				if c.Config, ok = stringMap(entry[name]); !ok {
					return fmt.Errorf("config: %s: connections.%s.config must be a table of strings", path, id)
				}
			case "transport":
				if c.Transport, ok = stringMap(entry[name]); !ok {
					return fmt.Errorf("config: %s: connections.%s.transport must be a table of strings", path, id)
				}
			case "capabilities":
				list, ok := entry[name].([]any)
				if !ok {
					return fmt.Errorf("config: %s: connections.%s.capabilities must be a list", path, id)
				}
				for _, item := range list {
					c.Capabilities = append(c.Capabilities, strings.TrimSpace(fmt.Sprint(item)))
				}
			case "credential_files":
				if c.CredentialFiles, ok = stringMap(entry[name]); !ok {
					return fmt.Errorf("config: %s: connections.%s.credential_files must be a table of strings", path, id)
				}
			default:
				return fmt.Errorf("config: %s: unknown connections.%s key %q", path, id, name)
			}
		}
		if c.Kind == "" {
			return fmt.Errorf("config: %s: connections.%s needs a kind", path, id)
		}
		h.Connections[id] = c
	}
	return nil
}

func stringMap(raw any) (map[string]string, bool) {
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(table))
	for k, v := range table {
		out[k] = strings.TrimSpace(fmt.Sprint(v))
	}
	return out, true
}

func hostScalar(env Env, dotted string, raw any) (string, error) {
	switch v := raw.(type) {
	case string:
		var missing string
		out := interpolation.ReplaceAllStringFunc(v, func(m string) string {
			name := m[2 : len(m)-1]
			value, ok := env(name)
			if !ok && missing == "" {
				missing = name
			}
			return value
		})
		if missing != "" {
			return "", fmt.Errorf("config: %s: environment variable %s is not set", dotted, missing)
		}
		return out, nil
	case bool:
		return strconv.FormatBool(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	}
	return "", fmt.Errorf("config: %s: must be a string, number or boolean", dotted)
}

func validateHostValue(d settings.HostDef, value string) error {
	switch d.Type {
	case settings.Int:
		if _, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err != nil {
			return fmt.Errorf("%s must be a whole number", d.Key)
		}
	case settings.Float:
		if _, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err != nil {
			return fmt.Errorf("%s must be a number", d.Key)
		}
	case settings.Bool:
		if value != "true" && value != "false" {
			return fmt.Errorf("%s must be true or false", d.Key)
		}
	case settings.Enum:
		if !slices.Contains(d.Choices, value) {
			return fmt.Errorf("%s must be one of %s", d.Key, strings.Join(d.Choices, ", "))
		}
	}
	return nil
}

func (h *Host) deriveHostConnections() {
	if _, ok := h.Connections["service/qbittorrent"]; ok {
		return
	}
	url := h.Values["media_qbittorrent_url"]
	if url == "" {
		return
	}
	c := HostConnection{
		Kind:         "service/qbittorrent",
		Label:        "qBittorrent",
		Config:       map[string]string{"url": url},
		Transport:    map[string]string{},
		Capabilities: []string{"api"},
	}
	if v := h.Values["qbittorrent_ca_file"]; v != "" {
		c.Transport["ca_file"] = v
	}
	if v := h.Values["qbittorrent_proxy"]; v != "" {
		c.Transport["proxy"] = v
	}
	h.Connections["service/qbittorrent"] = c
}

type hostKey struct{}

func WithHost(ctx context.Context, h Host) context.Context {
	return context.WithValue(ctx, hostKey{}, h)
}

func HostFrom(ctx context.Context) Host {
	h, _ := ctx.Value(hostKey{}).(Host)
	return h
}
