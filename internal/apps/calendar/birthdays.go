package calendar

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/textutil"
)

const (
	Birthdays      = "birthdays"
	birthdayPrefix = "birthday-"
)

type ReadOnly string

func (e ReadOnly) Error() string { return string(e) }

const errReadOnly = ReadOnly("the Birthdays calendar is read-only: its entries come from contact birthdays, so change the date on the contact")

func RefuseBirthday(uid string) error {
	if strings.HasPrefix(uid, birthdayPrefix) {
		return errReadOnly
	}
	return nil
}

func proposalError(err error) error {
	if errors.As(err, new(ReadOnly)) {
		return err
	}
	return ProposalError(err.Error())
}

func birthdayUID(card contacts.Card, date contacts.Date) string {
	who := card.UID
	if who == "" {
		who = textutil.Casefold(card.Name)
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%02d-%02d", who, date.Month, date.Day))
	return birthdayPrefix + hex.EncodeToString(sum[:8])
}

func birthdayDay(year, month, day int) Value {
	if month == int(time.February) && day == 29 && time.Date(year, time.March, 0, 0, 0, 0, 0, time.UTC).Day() != 29 {
		day = 28
	}
	return dateValue(year, time.Month(month), day)
}

func birthdayTitle(name string, born *int, year int) string {
	if born != nil && year > *born {
		return fmt.Sprintf("Birthday: %s (turns %d)", name, year-*born)
	}
	return "Birthday: " + name
}

func birthdayRows(dir string, ws, we time.Time, tz *time.Location) ([]Row, bool) {
	if info, err := os.Stat(dir); dir == "" || err != nil || !info.IsDir() {
		return nil, false
	}
	rows := []Row{}
	first, last := ws.In(tz).Year(), we.In(tz).Year()
	for _, card := range contacts.Load(dir) {
		for _, date := range card.Dates {
			if date.Kind != contacts.Birthday {
				continue
			}
			uid := birthdayUID(card, date)
			for year := first; year <= last; year++ {
				start := birthdayDay(year, date.Month, date.Day)
				end := dateValue(year, start.t.Month(), start.t.Day()+1)
				if !asDT(start, tz).Before(we) || !asDT(end, tz).After(ws) {
					continue
				}
				rows = append(rows, Row{UID: uid, Calendar: Birthdays, Start: start, End: end, Title: birthdayTitle(card.Name, date.Year, year)})
			}
		}
	}
	return rows, true
}
