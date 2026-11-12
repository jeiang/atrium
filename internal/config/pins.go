package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	ConfigEnv  = "ATRIUM_CONFIG"
	fileSuffix = "_file"
	dbKeyEnv   = "ATRIUM_DB_KEY"
)

var interpolation = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

type pin struct{ value, source string }

type Pins struct{ m map[string]pin }

func (p *Pins) Pin(key string) (value, source string, ok bool) {
	found, ok := p.m[key]
	return found.value, found.source, ok
}

func EnvName(d settings.Def) string {
	return "ATRIUM_" + strings.ToUpper(d.Module) + "_" + strings.ToUpper(d.Key)
}

func LoadPins(env Env, reg *settings.Registry) (*Pins, error) {
	if stripped(env, ConfigEnv) != "" {
		return nil, fmt.Errorf("config: %s is no longer read; host values come from %s, connection credentials from connections.<id>.credential_files.<slot>, and app keys from app_keys.action_link_key_file or app_keys.link_key_file", ConfigEnv, HostConfigEnv)
	}
	if strings.TrimSpace(get(env, "ANTHROPIC_API_KEY")) != "" {
		return nil, fmt.Errorf(`config: ANTHROPIC_API_KEY is no longer read; use connections."service/anthropic".credential_files.api_key`)
	}
	for _, d := range reg.Defs() {
		if err := refuseEnvName(env, d.Module, d.Key); err != nil {
			return nil, err
		}
	}
	for _, entry := range settings.Manifest() {
		if _, ok := reg.Lookup(entry.Key); ok {
			continue
		}
		if err := refuseEnvName(env, entry.Module, entry.Key); err != nil {
			return nil, err
		}
	}
	for _, retired := range settings.Retired {
		name := retired.EnvName()
		if strings.TrimSpace(get(env, name)) == "" && strings.TrimSpace(get(env, name+strings.ToUpper(fileSuffix))) == "" {
			continue
		}
		if retiredSecret(retired.Key) {
			return nil, fmt.Errorf("config: %s was removed; it has no replacement", name)
		}
		logx.Logger("atrium.config").Warn("ignoring a removed setting", "key", retired.Module+"."+retired.Key, "source", "env "+name)
	}
	pins := &Pins{m: map[string]pin{}}
	d, ok := reg.Lookup("key")
	if !ok {
		return pins, nil
	}
	value, source, found, err := readEnvValue(env, dbKeyEnv)
	if err != nil {
		return nil, err
	}
	if !found {
		return pins, nil
	}
	if err := reg.Validate(d.Key, value); err != nil {
		return nil, fmt.Errorf("config: %s: %w", source, err)
	}
	pins.m[d.Key] = pin{value: value, source: source}
	return pins, nil
}

func refuseEnvName(env Env, module, key string) error {
	name := "ATRIUM_" + strings.ToUpper(module) + "_" + strings.ToUpper(key)
	fileName := name + strings.ToUpper(fileSuffix)
	value := strings.TrimSpace(get(env, name))
	file := strings.TrimSpace(get(env, fileName))
	if value == "" && file == "" {
		return nil
	}
	entry, known := settings.ImportEntryFor(key)
	if known && entry.Module == "db" && entry.Key == "key" {
		return nil
	}
	if settings.HostKey(key) {
		return nil
	}
	destination := "the Connections page"
	if known {
		destination = settings.Destination(entry)
	}
	set := name
	if value == "" {
		set = fileName
	}
	return fmt.Errorf("config: %s is no longer read; use %s", set, destination)
}

func retiredSecret(key string) bool {
	switch key {
	case "anthropic_admin_key", "openai_admin_key":
		return true
	}
	return false
}

func readEnvValue(env Env, name string) (value, source string, found bool, err error) {
	direct := strings.TrimSpace(get(env, name))
	fileName := name + strings.ToUpper(fileSuffix)
	path, fileSet := env(fileName)
	if fileSet {
		if direct != "" {
			return "", "", false, fmt.Errorf("config: %s and %s are both set; set only one", name, fileName)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", "", false, fmt.Errorf("config: %s: %w", fileName, err)
		}
		return strings.TrimSpace(string(raw)), "env " + fileName, true, nil
	}
	if direct == "" {
		return "", "", false, nil
	}
	return direct, "env " + name, true, nil
}
