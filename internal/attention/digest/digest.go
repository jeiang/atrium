package digest

import (
	"context"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notify"
)

func Send(ctx context.Context, q db.Querier, env config.Env, now float64, sources []attention.Source) (notify.Sent, bool, error) {
	result, err := attention.Query(ctx, q, now, sources...)
	if err != nil {
		return notify.Sent{}, false, err
	}
	if len(result.Items) == 0 {
		return notify.Sent{}, false, nil
	}
	title, body := Text(result)
	sent, err := notify.Send(ctx, q, notify.Message{
		Channel: notify.Assistant, Title: title, Body: body,
		Now: func() float64 { return now },
	})
	return sent, true, err
}

func Text(result attention.Result) (string, string) {
	counts := make([]string, 0, len(result.Groups))
	for _, group := range result.Groups {
		counts = append(counts, fmt.Sprintf("%d %s", group.Count, strings.ToLower(group.Label)))
	}
	title := "atrium: " + strings.Join(counts, ", ")
	var lines []string
	for _, group := range result.Groups {
		lines = append(lines, group.Label+":")
		for i, item := range group.Items {
			if i == 3 {
				lines = append(lines, fmt.Sprintf("  … and %d more", group.Count-3))
				break
			}
			line := "  - " + item.Title
			if item.Detail != "" {
				line += " (" + item.Detail + ")"
			}
			lines = append(lines, line)
		}
	}
	return title, strings.Join(lines, "\n")
}
