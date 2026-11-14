package application

import (
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"time"

	grafanatools "github.com/jeiang/atrium/internal/grafana"
	"github.com/jeiang/atrium/internal/timex"
	"github.com/jeiang/atrium/internal/tool"
)

var (
	kinds      = []string{"prometheus", "logs"}
	dropFields = []string{"id", "tsNs"}
)

func attr(v any) (map[string]any, error) {
	d, ok := v.(map[string]any)
	if !ok {
		return nil, tool.Error("grafana returned an unexpected object")
	}
	return d, nil
}

func items(v any) ([]any, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, tool.Error("grafana returned an unexpected value")
	}
	return l, nil
}

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case int64:
		return v != 0
	case *big.Int:
		return v.Sign() != 0
	case float64:
		return v != 0
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func get(d map[string]any, key string) any {
	return d[key]
}

func orDefault(d map[string]any, key string, def any) any {
	if v, ok := d[key]; ok {
		return v
	}
	return def
}

func pyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func cell(v any, cellChars int) any {
	switch v := v.(type) {
	case float64:
		return roundTo(v, 6)
	case string:
		return tool.Clip(v, cellChars)
	case map[string]any, []any:
		return tool.Clip(fmt.Sprintf("%v", v), cellChars)
	}
	return v
}

func timeCell(v any, cellChars int) any {
	var ms float64
	switch n := v.(type) {
	case int64:
		ms = float64(n)
	case *big.Int:
		ms, _ = new(big.Float).SetInt(n).Float64()
	case float64:
		ms = n
	default:
		return cell(v, cellChars)
	}
	if !(ms > 1e11) {
		return cell(v, cellChars)
	}
	sec, us := timex.SplitTimestamp(ms / 1000)
	return time.Unix(sec, us*1000).UTC().Format("2006-01-02T15:04:05-07:00")
}

func roundTo(x float64, n int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	if n == 0 {
		return math.RoundToEven(x)
	}
	out, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', n, 64), 64)
	if err != nil {
		return x
	}
	return out
}

func sample(count, limit int) ([]int, error) {
	out := make([]int, 0, min(count, limit))
	if count <= limit {
		for i := range count {
			out = append(out, i)
		}
		return out, nil
	}
	if limit == 1 {
		return []int{0}, nil
	}
	for i := range limit {
		out = append(out, int(math.RoundToEven(float64(i*(count-1))/float64(limit-1))))
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func compactFrame(frameValue any, rowCap, cellChars int) (map[string]any, error) {
	frame, err := attr(frameValue)
	if err != nil {
		return nil, err
	}
	schema, err := attr(orDefault(frame, "schema", map[string]any{}))
	if err != nil {
		return nil, err
	}
	fieldList, err := items(orDefault(schema, "fields", []any{}))
	if err != nil {
		return nil, err
	}
	data, err := attr(orDefault(frame, "data", map[string]any{}))
	if err != nil {
		return nil, err
	}
	columns, err := items(orDefault(data, "values", []any{}))
	if err != nil {
		return nil, err
	}
	fields := make([]map[string]any, len(fieldList))
	var keep []int
	for i, f := range fieldList {
		if fields[i], err = attr(f); err != nil {
			return nil, err
		}
		name, named := get(fields[i], "name").(string)
		if i < len(columns) && (!named || !slices.Contains(dropFields, name)) {
			keep = append(keep, i)
		}
	}
	total := 0
	if len(columns) > 0 {
		first, err := items(columns[0])
		if err != nil {
			return nil, err
		}
		total = len(first)
	}
	picked, err := sample(total, rowCap)
	if err != nil {
		return nil, err
	}
	rows := make([]any, 0, len(picked))
	for _, r := range picked {
		row := make([]any, 0, len(keep))
		for _, i := range keep {
			column, err := items(columns[i])
			if err != nil {
				return nil, err
			}
			if r >= len(column) {
				return nil, tool.Error("grafana returned a short column")
			}
			if get(fields[i], "type") == "time" {
				row = append(row, timeCell(column[r], cellChars))
			} else {
				row = append(row, cell(column[r], cellChars))
			}
		}
		rows = append(rows, row)
	}
	labels := map[string]any{}
	names := make([]any, 0, len(keep))
	for _, i := range keep {
		if l := get(fields[i], "labels"); truthy(l) {
			ld, err := attr(l)
			if err != nil {
				return nil, err
			}
			for k, v := range ld {
				labels[k] = v
			}
		}
		names = append(names, orDefault(fields[i], "name", ""))
	}
	out := map[string]any{"fields": names, "rows": rows, "total_rows": int64(total)}
	if name := get(schema, "name"); truthy(name) {
		out["name"] = name
	}
	if len(labels) > 0 {
		out["labels"] = labels
	}
	return out, nil
}

func compactResults(payloadValue any, rowCap int, lim grafanatools.Limits) (any, error) {
	payload, err := attr(payloadValue)
	if err != nil {
		return nil, err
	}
	var frames []any
	if results := get(payload, "results"); truthy(results) {
		rd, err := attr(results)
		if err != nil {
			return nil, err
		}
		for _, p := range rd {
			result, err := attr(p)
			if err != nil {
				return nil, err
			}
			if e := get(result, "error"); truthy(e) {
				return nil, tool.Error("query error: " + tool.Clip(pyStr(e), lim.CellChars))
			}
			if f := get(result, "frames"); truthy(f) {
				list, err := items(f)
				if err != nil {
					return nil, err
				}
				frames = append(frames, list...)
			}
		}
	}
	series := make([]any, 0, min(len(frames), lim.Series))
	for _, f := range frames[:min(len(frames), lim.Series)] {
		c, err := compactFrame(f, rowCap, lim.CellChars)
		if err != nil {
			return nil, err
		}
		series = append(series, c)
	}
	return map[string]any{"series": series, "total_series": int64(len(frames))}, nil
}
