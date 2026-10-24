package config

import (
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	ConfigEnv  = "ATRIUM_CONFIG"
	fileSuffix = "_file"
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

func TOMLName(d settings.Def) string { return d.Module + "." + d.Key }

func LoadPins(env Env, reg *settings.Registry) (*Pins, error) {
	defs := reg.Defs()
	pins := &Pins{m: map[string]pin{}}
	if path := stripped(env, ConfigEnv); path != "" {
		if err := pins.readFile(env, reg, defs, path); err != nil {
			return nil, err
		}
	}
	for _, d := range defs {
		if d.Class == settings.Fixed {
			continue
		}
		found, ok, err := pinFromEnv(env, reg, d)
		if err != nil {
			return nil, err
		}
		if ok {
			pins.m[d.Key] = found
		}
	}
	livePins.Store(pins)
	return pins, nil
}

func pinFromEnv(env Env, reg *settings.Registry, d settings.Def) (pin, bool, error) {
	names := []string{EnvName(d)}
	if d.EnvAlias != "" {
		names = append(names, d.EnvAlias)
	}
	var found []pin
	var used []string
	for _, name := range names {
		value := get(env, name)
		if d.Secret {
			value = py.Strip(value)
			fileName := name + strings.ToUpper(fileSuffix)
			if path, set := env(fileName); set {
				if value != "" {
					return pin{}, false, fmt.Errorf("config: %s and %s are both set; set only one", name, fileName)
				}
				secret, err := os.ReadFile(path)
				if err != nil {
					return pin{}, false, fmt.Errorf("config: %s: %w", fileName, err)
				}
				value, name = py.Strip(string(secret)), fileName
				if err := reg.Validate(d.Key, value); err != nil {
					return pin{}, false, fmt.Errorf("config: %s: %w", name, err)
				}
				found, used = append(found, pin{value, "env " + name}), append(used, name)
				continue
			}
		}
		if value == "" {
			continue
		}
		if err := reg.Validate(d.Key, value); err != nil {
			return pin{}, false, fmt.Errorf("config: %s: %w", name, err)
		}
		found, used = append(found, pin{value, "env " + name}), append(used, name)
	}
	switch len(found) {
	case 0:
		return pin{}, false, nil
	case 1:
		return found[0], true, nil
	}
	return pin{}, false, fmt.Errorf("config: %s and %s are both set; set only one", used[0], used[1])
}

func (p *Pins) readFile(env Env, reg *settings.Registry, defs []settings.Def, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: %s: %w", ConfigEnv, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("config: %s: %w", path, err)
	}
	modules := map[string]map[string]settings.Def{}
	for _, d := range defs {
		if modules[d.Module] == nil {
			modules[d.Module] = map[string]settings.Def{}
		}
		modules[d.Module][d.Key] = d
	}
	for _, module := range slices.Sorted(maps.Keys(doc)) {
		keys, known := modules[module]
		table, isTable := doc[module].(map[string]any)
		if !known || !isTable {
			return fmt.Errorf("config: %s: unknown key %q", path, module)
		}
		for _, name := range slices.Sorted(maps.Keys(table)) {
			dotted := module + "." + name
			d, isFile, ok := keyOf(keys, name)
			if !ok {
				return fmt.Errorf("config: %s: unknown key %q", path, dotted)
			}
			if err := p.add(env, reg, d, path, dotted, isFile, table[name]); err != nil {
				return err
			}
		}
	}
	return nil
}

func keyOf(keys map[string]settings.Def, name string) (d settings.Def, isFile, ok bool) {
	if d, ok = keys[name]; ok {
		return d, false, true
	}
	if base, cut := strings.CutSuffix(name, fileSuffix); cut {
		d, ok = keys[base]
		return d, true, ok
	}
	return d, false, false
}

func (p *Pins) add(env Env, reg *settings.Registry, d settings.Def, path, dotted string, isFile bool, raw any) error {
	if d.Class == settings.Fixed {
		return fmt.Errorf("config: %s: %q is fixed and cannot be set", path, dotted)
	}
	if d.Secret && !isFile {
		return fmt.Errorf("config: %s: %q is a secret; set %s%s, %s or %s%s", path, dotted, TOMLName(d), fileSuffix, EnvName(d), EnvName(d), strings.ToUpper(fileSuffix))
	}
	if _, dup := p.m[d.Key]; dup {
		return fmt.Errorf("config: %s: %q is set both directly and through %s%s", path, TOMLName(d), TOMLName(d), fileSuffix)
	}
	value, err := scalar(env, dotted, raw)
	if err != nil {
		return err
	}
	if isFile {
		secret, err := os.ReadFile(value)
		if err != nil {
			return fmt.Errorf("config: %s: %w", dotted, err)
		}
		value = py.Strip(string(secret))
	}
	if err := reg.Validate(d.Key, value); err != nil {
		return fmt.Errorf("config: %s: %w", dotted, err)
	}
	p.m[d.Key] = pin{value, "config " + path + " " + dotted}
	return nil
}

func scalar(env Env, dotted string, raw any) (string, error) {
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
