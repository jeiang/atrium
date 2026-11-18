package calendar

import (
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/settingspage"
	"github.com/jeiang/atrium/internal/unit"
)

func Sections() []settingspage.Section {
	return []settingspage.Section{{
		Name: "calendar", Title: "Calendar", Module: unit.Calendar,
		Fields: []settingspage.Field{
			{Key: davmirror.AccountKey, Label: "Account"},
			{Key: davmirror.AppleIDKey, Label: "Apple ID"},
			{Key: davmirror.ContactsAppleIDKey, Label: "Contacts Apple ID"},
			{Key: ICloudCalendarKey, Label: "Calendar"},
			{Key: DefaultMinutesKey, Label: "Event minutes", Number: true, Whole: true},
			{Key: WorkHoursKey, Label: "Work hours"},
			{Key: WorkDaysKey, Label: "Work days"},
		},
	}}
}
