package coremodule

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/jeiang/atrium/internal/connections"
)

const (
	matrixSection       = "support-matrix"
	capabilitiesSection = "capabilities-and-dependencies"
	presetsSection      = "oidc-presets"
)

func integrationsDocProblems(reg *connections.Registry, presets []string, doc string) []string {
	anchors := map[string]bool{}
	tables := map[string]map[string]string{matrixSection: {}, capabilitiesSection: {}, presetsSection: {}}
	section := ""
	for line := range strings.Lines(doc) {
		line = strings.TrimRight(line, "\r\n")
		if title, ok := docHeading(line); ok {
			section = docAnchor(title)
			anchors[section] = true
			continue
		}
		rows, ok := tables[section]
		cells := docCells(line)
		if !ok || len(cells) < 2 {
			continue
		}
		key, value := cells[0], cells[1]
		if section != capabilitiesSection {
			key = cells[1]
		}
		if name, ok := docCode(key); ok {
			rows[name] = value
		}
	}
	var problems []string
	kinds := reg.Kinds()
	slices.Sort(kinds)
	for _, kind := range kinds {
		def, _ := reg.Lookup(kind)
		name := string(kind)
		if _, ok := tables[matrixSection][name]; !ok {
			problems = append(problems, fmt.Sprintf("the support matrix has no row for %s", name))
		}
		want := "`" + strings.Join(def.Offers, "`, `") + "`"
		if have, ok := tables[capabilitiesSection][name]; !ok {
			problems = append(problems, fmt.Sprintf("the capabilities table has no row for %s", name))
		} else if have != want {
			problems = append(problems, fmt.Sprintf("the capabilities table lists %s for %s, not %s", have, name, want))
		}
		if def.Docs != "" && !anchors[def.Docs] {
			problems = append(problems, fmt.Sprintf("%s links to #%s, which is not a heading", name, def.Docs))
		}
		delete(tables[matrixSection], name)
		delete(tables[capabilitiesSection], name)
	}
	for _, name := range presets {
		if _, ok := tables[presetsSection][name]; !ok {
			problems = append(problems, fmt.Sprintf("the OIDC presets table has no row for %s", name))
		}
		delete(tables[presetsSection], name)
	}
	for _, section := range []string{matrixSection, capabilitiesSection, presetsSection} {
		for _, name := range slices.Sorted(maps.Keys(tables[section])) {
			problems = append(problems, fmt.Sprintf("#%s lists %s, which atrium does not have", section, name))
		}
	}
	return problems
}

func docHeading(line string) (string, bool) {
	rest := strings.TrimLeft(line, "#")
	level := len(line) - len(rest)
	if level == 0 || level > 6 || !strings.HasPrefix(rest, " ") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func docAnchor(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func docCells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") || len(line) < 2 {
		return nil
	}
	cells := strings.Split(line[1:len(line)-1], "|")
	for i, cell := range cells {
		cells[i] = strings.TrimSpace(cell)
	}
	return cells
}

func docCode(cell string) (string, bool) {
	if len(cell) < 3 || cell[0] != '`' || cell[len(cell)-1] != '`' || strings.Count(cell, "`") != 2 {
		return "", false
	}
	return cell[1 : len(cell)-1], true
}
