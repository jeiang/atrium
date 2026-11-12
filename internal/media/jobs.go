package media

import (
	"github.com/jeiang/atrium/internal/scheduler"
)

const (
	JobChannels   = "media-channels"
	JobSweep      = "media-sweep"
	JobThumbnails = "media-thumbnails"

	KeyScheduleChannels   = "agent_schedule_media_channels"
	KeyScheduleSweep      = "agent_schedule_media_sweep"
	KeyScheduleThumbnails = "agent_schedule_media_thumbnails"
)

func Jobs() []scheduler.Definition {
	return []scheduler.Definition{
		{Key: JobChannels, Schedule: "*/30 * * * *", ScheduleSetting: KeyScheduleChannels, Needs: []string{module}},
		{Key: JobSweep, Schedule: "*/10 * * * *", ScheduleSetting: KeyScheduleSweep, Needs: []string{module}},
		{Key: JobThumbnails, Schedule: "daily 04:00", ScheduleSetting: KeyScheduleThumbnails, Needs: []string{module}},
	}
}
