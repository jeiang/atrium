package llm_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

func replay(t *testing.T, scenario string) string {
	t.Helper()
	return golden.Replay(t, golden.LoadScenario(t, "llm", scenario).Exchanges).URL + "/v1"
}

func TestGoldenJudgeFolder(t *testing.T) {
	url := replay(t, "router_folder")
	var rows []llm.FolderSender
	var neighbors []llm.Neighbor
	for i := range 10 {
		rows = append(rows, llm.FolderSender{Folder: "Alpha", FromDomain: "alpha.test"}, llm.FolderSender{Folder: "Beta", FromDomain: "beta.test"})
		neighbors = append(neighbors,
			llm.Neighbor{Folder: "Alpha", FromDomain: "alpha.test", Subject: fmt.Sprintf("Alpha invoice %d", i)},
			llm.Neighbor{Folder: "Beta", FromDomain: "beta.test", Subject: fmt.Sprintf("Beta newsletter %d", i)})
	}
	m := llm.Message{Subject: "October invoice", FromName: "New Sender", FromAddr: "new@sender.test"}
	folder, confident, err := llm.JudgeFolder(t.Context(), url, []string{"Alpha", "Beta"}, llm.FolderCatalog(rows), neighbors[:10], m, "Your invoice for October is attached.")
	if err != nil || folder != "Alpha" || !confident {
		t.Fatalf("JudgeFolder = %q, %v, %v", folder, confident, err)
	}
}

func TestGoldenJudgeFlag(t *testing.T) {
	url := replay(t, "flag_judgment")
	m := llm.Message{Subject: "Can you send the form by Friday?", FromName: "Pal", FromAddr: "pal@friends.test"}
	got, err := llm.JudgeFlag(t.Context(), url, m, "Hi, could you send me the signed form by Friday? Thanks")
	want := llm.Flags{PersonAsking: true, DeadlineOrPaymentDue: true, Flag: true}
	if err != nil || got != want {
		t.Fatalf("JudgeFlag = %+v, %v", got, err)
	}
}

func TestGoldenJudgeEvent(t *testing.T) {
	url := replay(t, "event_candidate")
	m := llm.Message{Subject: "Lunch", FromName: "Pal", FromAddr: "pal@friends.test"}
	got, err := llm.JudgeEvent(t.Context(), url, m, "Lunch Thursday noon at Rituals?")
	if err != nil || !got {
		t.Fatalf("JudgeEvent = %v, %v", got, err)
	}
}

type recordedChat struct {
	MaxTokens int `json:"max_tokens"`
	Messages  []struct {
		Content string `json:"content"`
	} `json:"messages"`
	ResponseFormat struct {
		JSONSchema struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	} `json:"response_format"`
}

type recordedReply struct {
	Body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	} `json:"body"`
}

func TestGoldenChatEveryFeature(t *testing.T) {
	chats := 0
	for _, entry := range golden.LoadManifest(t, "llm").Scenarios {
		s := golden.LoadScenario(t, "llm", entry.Name)
		if !strings.HasSuffix(s.Exchanges[0].Request.Path, "/chat/completions") {
			continue
		}
		chats++
		t.Run(entry.Name, func(t *testing.T) {
			url := golden.Replay(t, s.Exchanges).URL + "/v1"
			for _, x := range s.Exchanges {
				var req recordedChat
				var reply recordedReply
				if err := json.Unmarshal(x.Request.Body, &req); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(x.Response, &reply); err != nil {
					t.Fatal(err)
				}
				got, err := llm.ChatJSON(t.Context(), url, req.Messages[0].Content, req.Messages[1].Content, req.ResponseFormat.JSONSchema.Schema, req.MaxTokens)
				if err != nil {
					t.Fatal(err)
				}
				golden.EqualJSON(t, []byte(reply.Body.Choices[0].Message.Content), got)
			}
		})
	}
	if chats != 9 {
		t.Errorf("%d chat scenarios, want 9", chats)
	}
}

func TestSchemasKeepPropertyOrder(t *testing.T) {
	for _, c := range []struct {
		schema any
		want   string
	}{
		{llm.FlagSchema, `{"type":"object","properties":{"person_asking":{"type":"boolean"},"deadline_or_payment_due":{"type":"boolean"},"security_event":{"type":"boolean"},"flag":{"type":"boolean"}},"required":["person_asking","deadline_or_payment_due","security_event","flag"],"additionalProperties":false}`},
		{llm.FolderSchema([]string{"B", "A"}), `{"type":"object","properties":{"folder":{"type":"string","enum":["B","A","uncertain"]},"confident":{"type":"boolean"}},"required":["folder","confident"],"additionalProperties":false}`},
		{llm.EventSchema, `{"type":"object","properties":{"has_event":{"type":"boolean"}},"required":["has_event"],"additionalProperties":false}`},
	} {
		got, err := json.Marshal(c.schema)
		if err != nil || string(got) != c.want {
			t.Errorf("schema = %s, %v\nwant %s", got, err, c.want)
		}
	}
}

func TestFolderCatalogTopSenders(t *testing.T) {
	rows := []llm.FolderSender{
		{"Shop", "b.test"}, {"Shop", "a.test"}, {"Shop", "c.test"}, {"Shop", "d.test"}, {"Shop", "d.test"},
		{"Bills", "z.test"},
	}
	want := "- Bills (top senders: z.test)\n- Shop (top senders: d.test, a.test, b.test)"
	if got := llm.FolderCatalog(rows); got != want {
		t.Errorf("catalog = %q", got)
	}
}

func TestPromptsClipByCodePoint(t *testing.T) {
	subject := strings.Repeat("é", 120)
	body := strings.Repeat("ü", 1600)
	got := llm.FolderPrompt("", []llm.Neighbor{{Folder: "F", FromDomain: "d", Subject: subject}}, llm.Message{}, body)
	if !strings.Contains(got, ": "+strings.Repeat("é", 100)+"\n") || !strings.HasSuffix(got, "Body:\n"+strings.Repeat("ü", 1500)) {
		t.Errorf("prompt not clipped by code point: %q", got)
	}
}

func TestChatDownIsServiceUnavailable(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "loading model", http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	for _, c := range []struct{ url, msg string }{
		{"", "no service URL configured for '/chat/completions'"},
		{closed.URL, closed.URL + "/chat/completions: <urlopen error "},
		{failing.URL, failing.URL + "/chat/completions: HTTP Error 503: Service Unavailable"},
	} {
		_, err := llm.JudgeEvent(t.Context(), c.url, llm.Message{}, "")
		var su *httpx.ServiceUnavailable
		if !errors.As(err, &su) || !strings.HasPrefix(su.Error(), c.msg) {
			t.Errorf("%q: err = %v, want ServiceUnavailable %q", c.url, err, c.msg)
		}
	}
}

func TestMalformedRepliesAreNotServiceUnavailable(t *testing.T) {
	for _, c := range []struct {
		reply string
		kind  error
	}{
		{`not json`, httpx.ErrValue},
		{`{}`, httpx.ErrKey},
		{`{"choices": []}`, httpx.ErrIndex},
		{`{"choices": [{"message": {"content": null}}]}`, httpx.ErrType},
		{`{"choices": [{"message": {"content": "{\"folder\""}}]}`, httpx.ErrValue},
		{`{"choices": [{"message": {"content": "{}"}}]}`, httpx.ErrKey},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(c.reply))
		}))
		_, _, err := llm.JudgeFolder(t.Context(), srv.URL, nil, "", nil, llm.Message{}, "")
		srv.Close()
		var su *httpx.ServiceUnavailable
		if !errors.Is(err, c.kind) || errors.As(err, &su) {
			t.Errorf("%s: err = %v, want %v", c.reply, err, c.kind)
		}
	}
}

func TestConfidentIsPythonTruthiness(t *testing.T) {
	for reply, want := range map[string]bool{`1`: true, `0`: false, `"no"`: true, `""`: false, `null`: false, `[]`: false} {
		content, _ := json.Marshal(`{"folder": "A", "confident": ` + reply + `}`)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `{"choices": [{"message": {"content": %s}}]}`, content)
		}))
		_, got, err := llm.JudgeFolder(t.Context(), srv.URL, []string{"A"}, "", nil, llm.Message{}, "")
		srv.Close()
		if err != nil || got != want {
			t.Errorf("confident %s = %v, %v", reply, got, err)
		}
	}
}
