package protocol

import (
	"path"
	"regexp"
	"strings"
)

const SampleMaxSize int64 = 150 << 20

var sampleWord = regexp.MustCompile(`(?i)\bsample\b`)

func Sample(rel string, size int64) bool {
	if size < 0 || size >= SampleMaxSize {
		return false
	}
	if sampleWord.MatchString(path.Base(rel)) {
		return true
	}
	dir := strings.ToLower(path.Base(path.Dir(rel)))
	return dir == "sample" || dir == "samples"
}
