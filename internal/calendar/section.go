package calendar

import "github.com/jeiang/atrium/internal/integrations"

func init() {
	integrations.RegisterSection(integrations.Section{
		Name: "calendar", Title: "Calendar", Module: "calendar",
		Fields: []integrations.Field{
			{Key: AccountKey, Label: "Account"},
			{Key: AppleIDKey, Label: "Apple ID"},
			{Key: ICloudCalendarKey, Label: "Calendar"},
			{Key: DefaultMinutesKey, Label: "Event minutes", Number: true, Whole: true},
			{Key: WorkHoursKey, Label: "Work hours"},
			{Key: WorkDaysKey, Label: "Work days"},
		},
	})
}
