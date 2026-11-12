package classify

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/textutil"
)

const Uncertain = "uncertain"

const folderSystem = "You file email into mail folders for the mailbox owner. You get the allowed folders with their typical sender " +
	"domains, one message, and its 10 most similar already-filed messages (folder, sender domain, subject). " +
	"Pick the folder where this message belongs, following how similar messages and the same sender were filed. " +
	`If you cannot tell, answer folder="` + Uncertain + `". confident=true only if you are sure of the folder.`

type FolderSender struct {
	Folder, FromDomain string
	N                  int
}

type Neighbor struct{ Folder, FromDomain, Subject string }

func folderSchema(folders []string) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"folder":    map[string]any{"type": "string", "enum": append(slices.Clip(folders), Uncertain)},
			"confident": map[string]any{"type": "boolean"},
		},
		"required":             []string{"folder", "confident"},
		"additionalProperties": false,
	})
}

func FolderCatalog(ctx context.Context, rows []FolderSender) string {
	topSenders := endpoint.From(ctx).TopSenders
	byFolder := map[string]map[string]int{}
	for _, r := range rows {
		if byFolder[r.Folder] == nil {
			byFolder[r.Folder] = map[string]int{}
		}
		byFolder[r.Folder][r.FromDomain] += max(r.N, 1)
	}
	lines := make([]string, 0, len(byFolder))
	for _, folder := range slices.Sorted(maps.Keys(byFolder)) {
		counts := byFolder[folder]
		top := slices.SortedFunc(maps.Keys(counts), func(a, b string) int {
			return cmp.Or(cmp.Compare(counts[b], counts[a]), strings.Compare(a, b))
		})
		top = top[:min(len(top), topSenders)]
		lines = append(lines, fmt.Sprintf("- %s (top senders: %s)", folder, strings.Join(top, ", ")))
	}
	return strings.Join(lines, "\n")
}

func folderPrompt(ctx context.Context, catalog string, neighbors []Neighbor, m Message, body string) string {
	similar := make([]string, len(neighbors))
	for i, n := range neighbors {
		similar[i] = fmt.Sprintf("%d. [%s] from %s: %s", i+1, n.Folder, n.FromDomain, textutil.Clip(n.Subject, 100))
	}
	return "Allowed folders:\n" + catalog + "\n\nSimilar filed messages (folder in brackets):\n" + strings.Join(similar, "\n") + "\n\n" +
		"Message to file:\nSubject: " + m.Subject + "\nFrom: " + m.FromName + " <" + m.FromAddr + ">\n" +
		"Body:\n" + textutil.Clip(body, endpoint.From(ctx).LLMBodyChars)
}

func Askable(folder string) bool { return !strings.EqualFold(folder, Uncertain) }

func (s *Service) JudgeFolder(ctx context.Context, item classifier.Item, folders []string, catalog string, neighbors []Neighbor, m Message, body string) (string, bool, error) {
	schema, err := folderSchema(folders)
	if err != nil {
		return "", false, err
	}
	var answer struct {
		Folder    string `json:"folder"`
		Confident bool   `json:"confident"`
	}
	if err := s.judge(ctx, classifier.Folder, item, folderSystem, folderPrompt(ctx, catalog, neighbors, m, body), schema, &answer); err != nil {
		return "", false, err
	}
	if answer.Folder != Uncertain && !slices.Contains(folders, answer.Folder) {
		return "", false, fmt.Errorf("%w: folder %q is not in the supplied catalog", httpx.ErrPayload, answer.Folder)
	}
	return answer.Folder, answer.Confident, nil
}
