package tokens

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/jsonx"
)

const CharsPerToken = 4

var valueJSON = jsonx.Opts{NoEnsureASCII: true}

func Estimate(text string) int64 {
	return (int64(utf8.RuneCountInString(text)) + CharsPerToken - 1) / CharsPerToken
}

func EstimateValue(v any) int64 { return Estimate(jsonx.Dumps(v, valueJSON)) }

func EstimateContent(content []json.RawMessage) (int64, error) {
	values := make([]any, len(content))
	for i, raw := range content {
		if err := json.Unmarshal(raw, &values[i]); err != nil {
			return 0, err
		}
	}
	return EstimateValue(values), nil
}
