// Package llm is src/atrium/llm.py: JSON-schema chat completions against
// the local OpenAI-compatible model server and the mail judgments built on
// them.
package llm

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/py"
)

const (
	Model     = "qwen"
	Uncertain = "uncertain"
	MaxTokens = 80
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

var FlagFields = [...]string{"person_asking", "deadline_or_payment_due", "security_event", "flag"}

// Field is one member of an Object.
type Field struct {
	Key   string
	Value any
}

// Object is a JSON object that keeps its member order, as a Python dict
// does; llama.cpp generates schema properties in the order given.
type Object []Field

func (o Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(f.Key)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(f.Value)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

var boolean = Object{{"type", "boolean"}}

func flagSchema() Object {
	props := make(Object, len(FlagFields))
	for i, k := range FlagFields {
		props[i] = Field{k, boolean}
	}
	return Object{
		{"type", "object"},
		{"properties", props},
		{"required", FlagFields[:]},
		{"additionalProperties", false},
	}
}

var (
	FlagSchema  = flagSchema()
	EventSchema = Object{
		{"type", "object"},
		{"properties", Object{{"has_event", boolean}}},
		{"required", []string{"has_event"}},
		{"additionalProperties", false},
	}
)

func FolderSchema(folders []string) Object {
	return Object{
		{"type", "object"},
		{"properties", Object{
			{"folder", Object{{"type", "string"}, {"enum", append(slices.Clip(folders), Uncertain)}}},
			{"confident", boolean},
		}},
		{"required", []string{"folder", "confident"}},
		{"additionalProperties", false},
	}
}

// FolderSender is one filed message's folder and sender domain.
type FolderSender struct{ Folder, FromDomain string }

// Neighbor is one similar filed message.
type Neighbor struct{ Folder, FromDomain, Subject string }

// Message is the message being judged.
type Message struct{ Subject, FromName, FromAddr string }

func FolderCatalog(rows []FolderSender) string {
	byFolder := map[string]map[string]int{}
	for _, r := range rows {
		if byFolder[r.Folder] == nil {
			byFolder[r.Folder] = map[string]int{}
		}
		byFolder[r.Folder][r.FromDomain]++
	}
	lines := make([]string, 0, len(byFolder))
	for _, folder := range slices.Sorted(maps.Keys(byFolder)) {
		counts := byFolder[folder]
		top := slices.SortedFunc(maps.Keys(counts), func(a, b string) int {
			return cmp.Or(cmp.Compare(counts[b], counts[a]), strings.Compare(a, b))
		})
		top = top[:min(len(top), config.LLMTopSenders)]
		lines = append(lines, fmt.Sprintf("- %s (top senders: %s)", folder, strings.Join(top, ", ")))
	}
	return strings.Join(lines, "\n")
}

func FolderPrompt(catalog string, neighbors []Neighbor, m Message, body string) string {
	similar := make([]string, len(neighbors))
	for i, n := range neighbors {
		similar[i] = fmt.Sprintf("%d. [%s] from %s: %s", i+1, n.Folder, n.FromDomain, py.Clip(n.Subject, 100))
	}
	return "Allowed folders:\n" + catalog + "\n\nSimilar filed messages (folder in brackets):\n" + strings.Join(similar, "\n") + "\n\n" +
		"Message to file:\nSubject: " + m.Subject + "\nFrom: " + m.FromName + " <" + m.FromAddr + ">\n" +
		"Body:\n" + py.Clip(body, config.LLMBodyChars)
}

func messagePrompt(m Message, body string) string {
	return "From: " + m.FromName + " <" + m.FromAddr + ">\nSubject: " + m.Subject + "\nBody:\n" + py.Clip(body, config.LLMBodyChars)
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

// ChatJSON asks for a reply matching schema and returns the reply's JSON
// content; maxTokens <= 0 means MaxTokens.
func ChatJSON(ctx context.Context, url, system, user string, schema any, maxTokens int) (json.RawMessage, error) {
	if maxTokens <= 0 {
		maxTokens = MaxTokens
	}
	req := chatRequest{
		Model:       Model,
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
		return nil, fmt.Errorf("%w: %w", httpx.ErrValue, err)
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
		return nil, fmt.Errorf("%w: the JSON object must be str, not %s", httpx.ErrType, httpx.TypeName(content))
	}
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("%w: chat reply content is not JSON: %q", httpx.ErrValue, text)
	}
	return json.RawMessage(text), nil
}

func answer(raw json.RawMessage) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%w: %w", httpx.ErrValue, err)
	}
	return v, nil
}

func field(a any, k string) (bool, error) {
	v, err := httpx.Key(a, k)
	return httpx.Truthy(v), err
}

// JudgeFolder returns the folder the model picked and whether it is
// confident.
func JudgeFolder(ctx context.Context, url string, folders []string, catalog string, neighbors []Neighbor, m Message, body string) (string, bool, error) {
	raw, err := ChatJSON(ctx, url, FolderSystem, FolderPrompt(catalog, neighbors, m, body), FolderSchema(folders), MaxTokens)
	if err != nil {
		return "", false, err
	}
	a, err := answer(raw)
	if err != nil {
		return "", false, err
	}
	f, err := httpx.Key(a, "folder")
	if err != nil {
		return "", false, err
	}
	folder, ok := f.(string)
	if !ok {
		return "", false, fmt.Errorf("%w: folder is %s, not str", httpx.ErrType, httpx.TypeName(f))
	}
	confident, err := field(a, "confident")
	return folder, confident, err
}

// Flags is judge_flag's verdict, one bool per FlagFields entry.
type Flags struct {
	PersonAsking         bool `json:"person_asking"`
	DeadlineOrPaymentDue bool `json:"deadline_or_payment_due"`
	SecurityEvent        bool `json:"security_event"`
	Flag                 bool `json:"flag"`
}

func JudgeFlag(ctx context.Context, url string, m Message, body string) (Flags, error) {
	raw, err := ChatJSON(ctx, url, FlagSystem, messagePrompt(m, body), FlagSchema, MaxTokens)
	if err != nil {
		return Flags{}, err
	}
	a, err := answer(raw)
	if err != nil {
		return Flags{}, err
	}
	var f Flags
	for i, dst := range []*bool{&f.PersonAsking, &f.DeadlineOrPaymentDue, &f.SecurityEvent, &f.Flag} {
		if *dst, err = field(a, FlagFields[i]); err != nil {
			return Flags{}, err
		}
	}
	return f, nil
}

func JudgeEvent(ctx context.Context, url string, m Message, body string) (bool, error) {
	raw, err := ChatJSON(ctx, url, EventSystem, messagePrompt(m, body), EventSchema, MaxTokens)
	if err != nil {
		return false, err
	}
	a, err := answer(raw)
	if err != nil {
		return false, err
	}
	return field(a, "has_event")
}
