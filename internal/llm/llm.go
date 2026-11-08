package llm

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/textutil"
)

const (
	Uncertain = "uncertain"
)

const FolderSystem = "You file email into mail folders for the mailbox owner. You get the allowed folders with their typical sender " +
	"domains, one message, and its 10 most similar already-filed messages (folder, sender domain, subject). " +
	"Pick the folder where this message belongs, following how similar messages and the same sender were filed. " +
	`If you cannot tell, answer folder="` + Uncertain + `". confident=true only if you are sure of the folder.`

const FlagSystem = "You triage the mailbox owner's email and decide whether to flag it. Flag when a person (not an automated " +
	"sender) asks the owner something or needs their action, or there is a concrete deadline or payment due, or a " +
	"real fraud / sign-in / account-security event. Never flag statements, receipts, newsletters, promotions, or " +
	"routine notifications. Answer the JSON fields: person_asking (a human, not an automated sender, asks the owner " +
	"something or needs their action), deadline_or_payment_due (concrete deadline or payment due), security_event " +
	"(real fraud, sign-in or account-security event), and flag (final decision per the policy)."

const EventSystem = "You scan the mailbox owner's email for calendar events. has_event=true only when the message mentions a " +
	"specific event the owner themself would attend or must block time for (appointment, meeting, flight, " +
	"reservation, class, delivery window) with a concrete date or time. Newsletters, promotions, generic " +
	"deadlines, past events and events of other people are has_event=false."

var flagFields = [...]string{"person_asking", "deadline_or_payment_due", "security_event", "flag"}

var boolean = map[string]any{"type": "boolean"}

func flagSchema() map[string]any {
	props := make(map[string]any, len(flagFields))
	for _, k := range flagFields {
		props[k] = boolean
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             flagFields[:],
		"additionalProperties": false,
	}
}

var (
	flagSchemaValue = flagSchema()
	eventSchema     = map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"has_event": boolean},
		"required":             []string{"has_event"},
		"additionalProperties": false,
	}
)

func folderSchema(folders []string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"folder":    map[string]any{"type": "string", "enum": append(slices.Clip(folders), Uncertain)},
			"confident": boolean,
		},
		"required":             []string{"folder", "confident"},
		"additionalProperties": false,
	}
}

type FolderSender struct {
	Folder, FromDomain string
	N                  int
}

type Neighbor struct{ Folder, FromDomain, Subject string }

type Message struct{ Subject, FromName, FromAddr string }

func BodyChars(ctx context.Context) int { return endpoint.From(ctx).LLMBodyChars }

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

func FolderPrompt(ctx context.Context, catalog string, neighbors []Neighbor, m Message, body string) string {
	similar := make([]string, len(neighbors))
	for i, n := range neighbors {
		similar[i] = fmt.Sprintf("%d. [%s] from %s: %s", i+1, n.Folder, n.FromDomain, textutil.Clip(n.Subject, 100))
	}
	return "Allowed folders:\n" + catalog + "\n\nSimilar filed messages (folder in brackets):\n" + strings.Join(similar, "\n") + "\n\n" +
		"Message to file:\nSubject: " + m.Subject + "\nFrom: " + m.FromName + " <" + m.FromAddr + ">\n" +
		"Body:\n" + textutil.Clip(body, endpoint.From(ctx).LLMBodyChars)
}

func messagePrompt(ctx context.Context, m Message, body string) string {
	return "From: " + m.FromName + " <" + m.FromAddr + ">\nSubject: " + m.Subject + "\nBody:\n" + textutil.Clip(body, endpoint.From(ctx).LLMBodyChars)
}

type chatRequest struct {
	Model              string        `json:"model"`
	Messages           []chatMessage `json:"messages"`
	Temperature        int           `json:"temperature"`
	MaxTokens          int           `json:"max_tokens"`
	CachePrompt        bool          `json:"cache_prompt"`
	ChatTemplateKwargs struct {
		EnableThinking bool `json:"enable_thinking"`
	} `json:"chat_template_kwargs"`
	ResponseFormat struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string `json:"name"`
			Strict bool   `json:"strict"`
			Schema any    `json:"schema"`
		} `json:"json_schema"`
	} `json:"response_format"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func ChatJSON(ctx context.Context, url, system, user string, schema any, maxTokens int) (json.RawMessage, error) {
	s := endpoint.From(ctx)
	if maxTokens <= 0 {
		maxTokens = s.MaxTokens
	}
	req := chatRequest{
		Model:       s.ChatModel,
		Messages:    []chatMessage{{"system", system}, {"user", user}},
		MaxTokens:   maxTokens,
		CachePrompt: true,
	}
	req.ResponseFormat.Type = "json_schema"
	req.ResponseFormat.JSONSchema.Name = "answer"
	req.ResponseFormat.JSONSchema.Strict = true
	req.ResponseFormat.JSONSchema.Schema = schema
	raw, err := httpx.PostJSON(ctx, url+"/chat/completions", req)
	if err != nil {
		return nil, err
	}
	var reply any
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("%w: %w", httpx.ErrPayload, err)
	}
	choices, err := httpx.Key(reply, "choices")
	if err != nil {
		return nil, err
	}
	first, err := httpx.Index(choices, 0)
	if err != nil {
		return nil, err
	}
	message, err := httpx.Key(first, "message")
	if err != nil {
		return nil, err
	}
	content, err := httpx.Key(message, "content")
	if err != nil {
		return nil, err
	}
	text, ok := content.(string)
	if !ok {
		return nil, fmt.Errorf("%w: chat reply content is not a string", httpx.ErrPayload)
	}
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("%w: chat reply content is not JSON: %q", httpx.ErrPayload, text)
	}
	return json.RawMessage(text), nil
}

func decode(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: %w", httpx.ErrPayload, err)
	}
	return nil
}

type folderAnswer struct {
	Folder    string `json:"folder"`
	Confident bool   `json:"confident"`
}

func JudgeFolder(ctx context.Context, url string, folders []string, catalog string, neighbors []Neighbor, m Message, body string) (string, bool, error) {
	raw, err := ChatJSON(ctx, url, FolderSystem, FolderPrompt(ctx, catalog, neighbors, m, body), folderSchema(folders), 0)
	if err != nil {
		return "", false, err
	}
	var answer folderAnswer
	if err := decode(raw, &answer); err != nil {
		return "", false, err
	}
	return answer.Folder, answer.Confident, nil
}

type Flags struct {
	PersonAsking         bool `json:"person_asking"`
	DeadlineOrPaymentDue bool `json:"deadline_or_payment_due"`
	SecurityEvent        bool `json:"security_event"`
	Flag                 bool `json:"flag"`
}

func JudgeFlag(ctx context.Context, url string, m Message, body string) (Flags, error) {
	raw, err := ChatJSON(ctx, url, FlagSystem, messagePrompt(ctx, m, body), flagSchemaValue, 0)
	if err != nil {
		return Flags{}, err
	}
	var flags Flags
	if err := decode(raw, &flags); err != nil {
		return Flags{}, err
	}
	return flags, nil
}

func JudgeEvent(ctx context.Context, url string, m Message, body string) (bool, error) {
	raw, err := ChatJSON(ctx, url, EventSystem, messagePrompt(ctx, m, body), eventSchema, 0)
	if err != nil {
		return false, err
	}
	var answer struct {
		HasEvent bool `json:"has_event"`
	}
	if err := decode(raw, &answer); err != nil {
		return false, err
	}
	return answer.HasEvent, nil
}
