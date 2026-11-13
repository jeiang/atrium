package importv2

import (
	"bufio"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/settings"
)

var sourceInterpolation = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

type sourcePins struct {
	m       map[string]settings.ImportPin
	unknown []string
}

func (p *sourcePins) importPins() map[string]settings.ImportPin { return p.m }

func readSourcePins(env config.Env, configPath, envPath string) (*sourcePins, error) {
	p := &sourcePins{m: map[string]settings.ImportPin{}}
	file, err := readSourceEnv(envPath)
	if err != nil {
		return nil, err
	}
	lookup := func(name string) (string, string, bool) {
		if value, ok := file[name]; ok && value != "" {
			return value, "source env " + envPath + " " + name, true
		}
		if value, ok := env(name); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), "env " + name, true
		}
		return "", "", false
	}
	if configPath != "" {
		if err := p.readSourceConfig(lookup, configPath); err != nil {
			return nil, err
		}
	}
	for _, entry := range append(settings.Manifest(), retiredEntries()...) {
		name := sourceEnvName(entry)
		fileName := name + "_FILE"
		if strings.HasSuffix(entry.Key, "_file") {
			if raw, ok := file[name]; ok && raw != "" {
				value, err := sourcePinValue(entry, raw, "source env "+envPath+" "+name, false)
				if err != nil {
					return nil, err
				}
				p.m[entry.Key] = value
				continue
			}
			if path, ok := file[fileName]; ok && path != "" {
				value, err := sourcePinValue(entry, path, "source env "+envPath+" "+fileName, true)
				if err != nil {
					return nil, err
				}
				p.m[entry.Key] = value
			}
			continue
		}
		if path, ok := file[fileName]; ok && path != "" {
			value, err := sourcePinValue(entry, path, "source env "+envPath+" "+fileName, true)
			if err != nil {
				return nil, err
			}
			p.m[entry.Key] = value
			continue
		}
		if raw, ok := file[name]; ok && raw != "" {
			value, err := sourcePinValue(entry, raw, "source env "+envPath+" "+name, false)
			if err != nil {
				return nil, err
			}
			p.m[entry.Key] = value
		}
	}
	return p, nil
}

func (p *sourcePins) readSourceConfig(lookup func(string) (string, string, bool), path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("db import-v2: source config %s: %w", path, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("db import-v2: source config %s: %w", path, err)
	}
	for _, module := range slices.Sorted(maps.Keys(doc)) {
		table, ok := doc[module].(map[string]any)
		if !ok {
			p.unknown = append(p.unknown, module)
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(table)) {
			entry, known := sourceEntry(module, name)
			isFile := false
			if !known {
				base, cut := strings.CutSuffix(name, "_file")
				if !cut {
					p.unknown = append(p.unknown, module+"."+name)
					continue
				}
				entry, known = sourceEntry(module, base)
				isFile = true
			}
			if !known {
				p.unknown = append(p.unknown, module+"."+name)
				continue
			}
			text, err := sourceScalar(lookup, module+"."+name, table[name])
			if err != nil {
				return fmt.Errorf("db import-v2: source config %s: %w", path, err)
			}
			value, err := sourcePinValue(entry, text, "config "+path+" "+module+"."+name, isFile)
			if err != nil {
				return fmt.Errorf("db import-v2: source config %s: %w", path, err)
			}
			p.m[entry.Key] = value
		}
	}
	return nil
}

func sourcePinValue(entry settings.ImportEntry, raw, source string, isFile bool) (settings.ImportPin, error) {
	if settings.ImportSecretName(entry.Key) {
		return settings.ImportPin{Source: source}, nil
	}
	value := raw
	if isFile {
		content, err := os.ReadFile(raw)
		if err != nil {
			return settings.ImportPin{}, fmt.Errorf("%s: %w", source, err)
		}
		value = strings.TrimSpace(string(content))
	}
	return settings.ImportPin{Value: value, Source: source}, nil
}

func sourceInterpolate(lookup func(string) (string, string, bool), source, value string) (string, error) {
	var missing string
	out := sourceInterpolation.ReplaceAllStringFunc(value, func(m string) string {
		name := m[2 : len(m)-1]
		found, _, ok := lookup(name)
		if !ok && missing == "" {
			missing = name
		}
		return found
	})
	if missing != "" {
		return "", fmt.Errorf("%s: environment variable %s is not set", source, missing)
	}
	return out, nil
}

func sourceScalar(lookup func(string) (string, string, bool), dotted string, raw any) (string, error) {
	switch v := raw.(type) {
	case string:
		return sourceInterpolate(lookup, dotted, v)
	case bool:
		return strconv.FormatBool(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	}
	return "", fmt.Errorf("%s: must be a string, number or boolean", dotted)
}

func readSourceEnv(path string) (map[string]string, error) {
	out := map[string]string{}
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("db import-v2: source environment %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("db import-v2: source environment %s: line %d is not NAME=VALUE", path, lineNo)
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("db import-v2: source environment %s: %w", path, err)
	}
	return out, nil
}

func sourceEnvName(entry settings.ImportEntry) string {
	return "ATRIUM_" + strings.ToUpper(entry.Module) + "_" + strings.ToUpper(entry.Key)
}

func sourceEntry(module, key string) (settings.ImportEntry, bool) {
	entry, ok := settings.ImportEntryFor(key)
	if !ok || entry.Module != module {
		return settings.ImportEntry{}, false
	}
	return entry, true
}

func retiredEntries() []settings.ImportEntry {
	var out []settings.ImportEntry
	for _, key := range []string{"oidc_access_group", "oidc_admin_group", "oidc_admin_emails", "oidc_admin_domains"} {
		if entry, ok := settings.ImportEntryFor(key); ok {
			out = append(out, entry)
		}
	}
	return out
}
