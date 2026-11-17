package calendar

import "regexp"

var refUID = regexp.MustCompile(`^[A-Za-z0-9@._+-]{1,200}$`)

func EventRef(day, uid string) string {
	if !refUID.MatchString(uid) {
		return ""
	}
	return "event:" + day + ":" + uid
}
