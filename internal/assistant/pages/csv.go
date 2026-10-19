package pages

import (
	"errors"
	"strings"
)

type csvState int

const (
	startRecord csvState = iota
	startField
	inField
	inQuotedField
	quoteInQuotedField
	eatCRNL
)

const csvEOL = -1

type csvReader struct {
	state  csvState
	field  strings.Builder
	fields []string
	err    error
}

func (c *csvReader) save() {
	c.fields = append(c.fields, c.field.String())
	c.field.Reset()
}

func (c *csvReader) endOrEat(ch rune) {
	c.save()
	if ch == csvEOL {
		c.state = startRecord
	} else {
		c.state = eatCRNL
	}
}

func newline(ch rune) bool { return ch == '\n' || ch == '\r' || ch == csvEOL }

func (c *csvReader) process(ch rune) {
	switch c.state {
	case startRecord:
		if ch == csvEOL {
			return
		}
		if ch == '\n' || ch == '\r' {
			c.state = eatCRNL
			return
		}
		c.state = startField
		c.process(ch)
	case startField:
		switch {
		case newline(ch):
			c.endOrEat(ch)
		case ch == '"':
			c.state = inQuotedField
		case ch == ',':
			c.save()
		default:
			c.field.WriteRune(ch)
			c.state = inField
		}
	case inField:
		switch {
		case newline(ch):
			c.endOrEat(ch)
		case ch == ',':
			c.save()
			c.state = startField
		default:
			c.field.WriteRune(ch)
		}
	case inQuotedField:
		switch ch {
		case csvEOL:
		case '"':
			c.state = quoteInQuotedField
		default:
			c.field.WriteRune(ch)
		}
	case quoteInQuotedField:
		switch {
		case ch == '"':
			c.field.WriteRune('"')
			c.state = inQuotedField
		case ch == ',':
			c.save()
			c.state = startField
		case newline(ch):
			c.endOrEat(ch)
		default:
			c.field.WriteRune(ch)
			c.state = inField
		}
	case eatCRNL:
		switch ch {
		case '\n', '\r':
		case csvEOL:
			c.state = startRecord
		default:
			c.err = errors.New("new-line character seen in unquoted field - do you need to open the file with newline=''?")
		}
	}
}

func pyCSV(text string) ([][]string, error) {
	rows := [][]string{}
	c := &csvReader{}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i := 0; i < len(lines); {
		c.fields = []string{}
		for {
			if i == len(lines) {
				if c.field.Len() != 0 || c.state == inQuotedField {
					c.save()
					rows = append(rows, c.fields)
				}
				return rows, nil
			}
			for _, ch := range lines[i] {
				if ch == 0 {
					return nil, errors.New("line contains NUL")
				}
				if c.process(ch); c.err != nil {
					return nil, c.err
				}
			}
			i++
			if c.process(csvEOL); c.err != nil {
				return nil, c.err
			}
			if c.state == startRecord {
				break
			}
		}
		rows = append(rows, c.fields)
	}
	return rows, nil
}
