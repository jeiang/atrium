package calendar

import "github.com/jeiang/atrium/internal/settingspage"

func Sections() []settingspage.Section {
	return []settingspage.Section{{
		Name: "calendar", Title: "Calendar", Module: "calendar",
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

func init() {
	for _, section := range Sections() {
		settingspage.RegisterSection(section)
	}
}
