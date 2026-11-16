package calendar

import (
	"github.com/jeiang/atrium/internal/settingspage"
	"github.com/jeiang/atrium/internal/unit"
)

func Sections() []settingspage.Section {
	return []settingspage.Section{{
		Name: "calendar", Title: "Calendar", Module: unit.Calendar,
		Fields: []settingspage.Field{
			{Key: AccountKey, Label: "Account"},
			{Key: AppleIDKey, Label: "Apple ID"},
			{Key: ICloudCalendarKey, Label: "Calendar"},
			{Key: DefaultMinutesKey, Label: "Event minutes", Number: true, Whole: true},
			{Key: WorkHoursKey, Label: "Work hours"},
			{Key: WorkDaysKey, Label: "Work days"},
		},
	}}
}
