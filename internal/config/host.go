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
	Sources     map[string]string
	Connections map[string]HostConnection
}

func (h Host) Value(key string) string { return h.Values[key] }

func (h Host) Connection(id string) (HostConnection, bool) {
	c, ok := h.Connections[id]
	return c, ok
}

func (h Host) Pin(key string) (value, source string, ok bool) {
	source, ok = h.Sources[key]
	if !ok {
		return "", "", false
	}
	return h.Values[key], source, true
}

func (h Host) LogValue() string {
	return fmt.Sprintf("%d host values, %d connections", len(h.Values), len(h.Connections))
}

func HostFile(env Env) string { return stripped(env, HostConfigEnv) }

func refuseRemovedSelectors(env Env) error {
	for _, key := range []string{"oidc_access_group", "oidc_admin_group", "oidc_admin_emails", "oidc_admin_domains"} {
		entry, removed := settings.ImportEntryFor(key)
		if !removed {
			continue
		}
		destination := settings.Destination(entry)
		name := "ATRIUM_AUTH_" + strings.ToUpper(key)
		if _, ok := env(name); ok {
			return fmt.Errorf("config: %s was removed; use %s", name, destination)
		}
		if _, ok := env(name + "_FILE"); ok {
			return fmt.Errorf("config: %s was removed; use %s", name+"_FILE", destination)
		}
	}
	return nil
}

func LoadHost(env Env, reg *settings.Registry, filename string) (Host, error) {
	defs := reg.HostDefs()
	h := Host{Values: map[string]string{}, Sources: map[string]string{}, Connections: map[string]HostConnection{}}
	for _, d := range defs {
		h.Values[d.Key] = d.Default
	}
	if err := refuseRemovedSelectors(env); err != nil {
		return Host{}, err
	}
	if filename != "" {
		if err := h.readFile(env, reg, defs, filename); err != nil {
			return Host{}, err
		}
	}
	for _, d := range defs {
		if err := h.readEnv(env, reg, d); err != nil {
			return Host{}, err
		}
	}
	h.deriveHostConnections()
	if err := h.validateAppKeys(); err != nil {
		return Host{}, err
	}
	return h, nil
}

func (h *Host) validateAppKeys() error {
	for _, key := range []string{"action_link_key_file", "link_key_file"} {
		path := strings.TrimSpace(h.Values[key])
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("config: app_keys.%s: %w", key, err)
		}
		if strings.TrimSpace(string(raw)) == "" {
			return fmt.Errorf("config: app_keys.%s: %s is empty", key, path)
		}
	}
	return nil
}

func (h *Host) readEnv(env Env, reg *settings.Registry, d settings.HostDef) error {
	if err := refuseEnvName(env, reg, d.Module, d.Key); err != nil {
		return err
	}
	name := "ATRIUM_" + strings.ToUpper(d.Module) + "_" + strings.ToUpper(d.Key)
	fileName := name + strings.ToUpper(fileSuffix)
	value := strings.TrimSpace(get(env, name))
	path, fileSet := env(fileName)
	if d.Secret {
		if value != "" {
			return fmt.Errorf("config: %s is a secret; set %s", name, fileName)
		}
		if !fileSet {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("config: %s: %w", fileName, err)
		}
		value = strings.TrimSpace(string(raw))
	} else if fileSet {
		if value != "" {
			return fmt.Errorf("config: %s and %s are both set; set only one", name, fileName)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("config: %s: %w", fileName, err)
		}
		value = strings.TrimSpace(string(raw))
	}
	if value == "" {
		return nil
	}
	if err := validateHostValue(reg, d, value); err != nil {
		return fmt.Errorf("config: %s: %w", name, err)
	}
	h.Values[d.Key] = value
	h.Sources[d.Key] = "env " + name
	return nil
}

func (h *Host) readFile(env Env, reg *settings.Registry, defs []settings.HostDef, path string) error {
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
			if err := h.readConnections(env, path, doc[module]); err != nil {
				return err
			}
			continue
		}
		keys, known := byModule[module]
		table, isTable := doc[module].(map[string]any)
		if !isTable {
			return fmt.Errorf("config: %s: unknown host key %q", path, module)
		}
		if !known {
			for _, name := range slices.Sorted(maps.Keys(table)) {
				if err := hostKeyRefusal(path, module, name); err != nil {
					return err
				}
			}
			return fmt.Errorf("config: %s: unknown host key %q", path, module)
		}
		for _, name := range slices.Sorted(maps.Keys(table)) {
			d, isFile, ok := hostKeyOf(keys, name)
			if !ok {
				if err := hostKeyRefusal(path, module, name); err != nil {
					return err
				}
				return fmt.Errorf("config: %s: unknown host key %q", path, module+"."+name)
			}
			if d.Secret && !isFile {
				return fmt.Errorf("config: %s: %s is a secret; set %s_file", path, module+"."+name, module+"."+d.Key)
			}
			value, err := hostScalar(env, module+"."+name, table[name])
			if err != nil {
				return err
			}
			if isFile {
				raw, err := os.ReadFile(value)
				if err != nil {
					return fmt.Errorf("config: %s: %w", module+"."+name, err)
				}
				value = strings.TrimSpace(string(raw))
			}
			if err := validateHostValue(reg, d, value); err != nil {
				return fmt.Errorf("config: %s: %s: %w", path, module+"."+name, err)
			}
			h.Values[d.Key] = value
			h.Sources[d.Key] = "host " + path + " " + module + "." + name
		}
	}
	return nil
}

func hostKeyOf(keys map[string]settings.HostDef, name string) (d settings.HostDef, isFile, ok bool) {
	if d, ok = keys[name]; ok {
		return d, false, true
	}
	if base, cut := strings.CutSuffix(name, "_file"); cut {
		d, ok = keys[base]
		return d, true, ok
	}
	return d, false, false
}

func hostKeyRefusal(path, module, name string) error {
	entry, ok := settings.ImportEntryFor(name)
	if base, cut := strings.CutSuffix(name, "_file"); !ok && cut {
		entry, ok = settings.ImportEntryFor(base)
	}
	if !ok || entry.Module != module {
		return nil
	}
	if entry.Action == settings.ImportDrop && entry.Class == settings.ImportOperator {
		return fmt.Errorf("config: %s: %s.%s was removed; use %s", path, module, name, settings.Destination(entry))
	}
	if entry.Module == "db" && entry.Key == "key" {
		return fmt.Errorf("config: %s: db.key is the database key; use ATRIUM_DB_KEY_FILE", path)
	}
	return fmt.Errorf("config: %s: %s.%s is not host configuration; use %s", path, module, name, settings.Destination(entry))
}

func (h *Host) readConnections(env Env, path string, raw any) error {
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
		var err error
		if c.Config, err = expandMap(env, path, id, "config", c.Config); err != nil {
			return err
		}
		if c.Transport, err = expandMap(env, path, id, "transport", c.Transport); err != nil {
			return err
		}
		if c.CredentialFiles, err = expandMap(env, path, id, "credential_files", c.CredentialFiles); err != nil {
			return err
		}
		for _, slot := range slices.Sorted(maps.Keys(c.CredentialFiles)) {
			f, err := os.Open(c.CredentialFiles[slot])
			if err != nil {
				return fmt.Errorf("config: %s: connections.%s.credential_files.%s: %w", path, id, slot, err)
			}
			_ = f.Close()
		}
		h.Connections[id] = c
	}
	return nil
}

func expandMap(env Env, path, id, field string, m map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(m))
	for k, v := range m {
		value, err := expand(env, v)
		if err != nil {
			return nil, fmt.Errorf("config: %s: connections.%s.%s.%s: %w", path, id, field, k, err)
		}
		out[k] = value
	}
	return out, nil
}

func expand(env Env, value string) (string, error) {
	var missing string
	out := interpolation.ReplaceAllStringFunc(value, func(m string) string {
		name := m[2 : len(m)-1]
		replacement, ok := env(name)
		if !ok && missing == "" {
			missing = name
		}
		return replacement
	})
	if missing != "" {
		return "", fmt.Errorf("environment variable %s is not set", missing)
	}
	return out, nil
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
		out, err := expand(env, v)
		if err != nil {
			return "", fmt.Errorf("config: %s: %w", dotted, err)
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

func validateHostValue(reg *settings.Registry, d settings.HostDef, value string) error {
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
	if err := reg.ValidateHost(d.Key, value); err != nil {
		return fmt.Errorf("%s %w", d.Key, err)
	}
	return nil
}

func (h *Host) deriveHostConnections() {
	h.deriveService("service/anthropic", "Anthropic", map[string]string{
		"url": "https://api.anthropic.com",
	}, nil, []string{"models"}, false)
	if _, declared := h.Connections["service/weather"]; declared {
		h.deriveService("service/weather", "Open-Meteo", map[string]string{
			"url":           settings.DefaultOf("weather_open_meteo_url"),
			"geocoding_url": settings.DefaultOf("weather_geocoding_url"),
		}, nil, []string{"forecast", "geocoding"}, false)
	}
	if _, declared := h.Connections["service/webfetch"]; declared {
		h.deriveService("service/webfetch", "Web fetch", map[string]string{
			"url": "https://example.com/",
		}, nil, []string{"egress"}, false)
	}
	if _, declared := h.Connections["service/tmdb"]; declared {
		h.deriveService("service/tmdb", "TMDB", map[string]string{
			"url":        settings.DefaultOf("media_tmdb_url"),
			"images_url": settings.DefaultOf("media_tmdb_images_url"),
		}, nil, []string{"api", "images"}, false)
	}
	if _, declared := h.Connections["service/eztv"]; declared {
		h.deriveService("service/eztv", "EZTV", map[string]string{
			"url": settings.DefaultOf("media_eztv_url"),
		}, nil, []string{"indexer"}, false)
	}
	if _, declared := h.Connections["service/embedding"]; declared {
		h.deriveService("service/embedding", "Embedding", map[string]string{
			"model": settings.DefaultOf("mail_embed_model"),
		}, nil, []string{"embeddings"}, false)
	}
	if _, declared := h.Connections["service/ship24"]; declared {
		h.deriveService("service/ship24", "Ship24", map[string]string{
			"url": settings.DefaultOf("mail_ship24_url"),
		}, nil, []string{"api"}, false)
	}
}

func (h *Host) deriveService(id, label string, config, transport map[string]string, capabilities []string, override bool) {
	c, ok := h.Connections[id]
	if !ok {
		c = HostConnection{Kind: id, Label: label}
	}
	if c.Kind == "" {
		c.Kind = id
	}
	if c.Label == "" {
		c.Label = label
	}
	if c.Config == nil {
		c.Config = map[string]string{}
	}
	if c.Transport == nil {
		c.Transport = map[string]string{}
	}
	for key, value := range config {
		if value == "" {
			continue
		}
		if _, set := c.Config[key]; set && !override {
			continue
		}
		c.Config[key] = value
	}
	for key, value := range transport {
		if value == "" {
			continue
		}
		if _, set := c.Transport[key]; set && !override {
			continue
		}
		c.Transport[key] = value
	}
	if len(c.Capabilities) == 0 {
		c.Capabilities = capabilities
	}
	h.Connections[id] = c
}

type combinedPins struct {
	host   Host
	legacy settings.PinSource
}

func (p combinedPins) Pin(key string) (value, source string, ok bool) {
	if value, source, ok := p.host.Pin(key); ok {
		return value, source, true
	}
	if p.legacy != nil {
		return p.legacy.Pin(key)
	}
	return "", "", false
}

func CombinePins(host Host, legacy settings.PinSource) settings.PinSource {
	return combinedPins{host: host, legacy: legacy}
}

type hostKey struct{}

func WithHost(ctx context.Context, h Host) context.Context {
	return context.WithValue(ctx, hostKey{}, h)
}

func HostFrom(ctx context.Context) Host {
	h, _ := ctx.Value(hostKey{}).(Host)
	return h
}
