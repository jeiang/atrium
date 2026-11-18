package urlx

import (
	"regexp"
	"strconv"
)

const (
	ClientMedia    = "/apps/media"
	ClientTorrents = "/apps/torrents"
)

var clientCard = regexp.MustCompile(`^/(?:proposals|drafts|chat|knowledge|assistant/routines|apps/mail/message)/[0-9]+$`)

func ClientKnowledge(id int64) string { return "/knowledge/" + strconv.FormatInt(id, 10) }

func ClientMailMessage(id int64) string { return "/apps/mail/message/" + strconv.FormatInt(id, 10) }

func ClientRoutine(jobID int64) string { return "/assistant/routines/" + strconv.FormatInt(jobID, 10) }

func ClientPage(path string) bool {
	return path == ClientMedia || path == ClientTorrents || clientCard.MatchString(path)
}
