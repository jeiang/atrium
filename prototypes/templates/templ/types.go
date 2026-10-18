package main

import "github.com/a-h/templ"

// Typed mirrors of data.json (the Jinja template contexts). In Jinja these shapes are implicit;
// templ needs every field declared. "Sum types" (chat item kinds, proposal previews) are tagged
// unions: one struct holding the union of all fields.

type Toast struct {
	Category    string           `json:"category"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Duration    *int             `json:"duration"`
	Action      *Button          `json:"action"`
	Cancel      *Button          `json:"cancel"`
	Icon        templ.Component  `json:"-"` // custom icon (trusted component)
	Attrs       templ.Attributes `json:"-"`
}

type Button struct {
	Label   string `json:"label"`
	Onclick string `json:"onclick"`
	Href    string `json:"href"`
	URL     string `json:"url"`
}

// Base is what base.html reads from the context (every page needs it).
type Base struct {
	Page         string
	PendingCount int
	Paused       bool
	Mode         string
	Toast        *Toast
}

type ThreadPageData struct {
	View          View      `json:"view"`
	Projects      []Project `json:"projects"`
	Project       *Project  `json:"project"`
	Efforts       []string  `json:"efforts"`
	ContextLimits []int     `json:"context_limits"`
	Work          Work      `json:"work"`
	PendingCount  int       `json:"pending_count"`
	Paused        bool      `json:"paused"`
	Mode          string    `json:"mode"`
	Toast         *Toast    `json:"toast"`
}

type Project struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type View struct {
	Thread          Thread       `json:"thread"`
	Active          *Active      `json:"active"`
	BranchedFrom    *BranchedRef `json:"branched_from"`
	Meter           Meter        `json:"meter"`
	Cost            float64      `json:"cost"`
	LastOperatorSeq int          `json:"last_operator_seq"`
	Items           []Item       `json:"items"`
	Runs            []Run        `json:"runs"`
	Queued          []Item       `json:"queued"` // queued entries are rendered as Item{Kind:"queued"}
}

type Thread struct {
	ID              int     `json:"id"`
	Title           string  `json:"title"`
	Kind            string  `json:"kind"`
	CreatedAt       float64 `json:"created_at"`
	ProjectID       int     `json:"project_id"`
	ParentThreadID  int     `json:"parent_thread_id"`
	BranchedFromSeq int     `json:"branched_from_seq"`
	Effort          string  `json:"effort"`
	Pinned          int     `json:"pinned"`
	Archived        int     `json:"archived"`
	CodeSandbox     int     `json:"code_sandbox"`
}

type Active struct {
	ID    int    `json:"id"`
	State string `json:"state"`
}

type BranchedRef struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

type Meter struct {
	Limit     int     `json:"limit"`
	CompactAt int     `json:"compact_at"`
	Tokens    int     `json:"tokens"`
	Share     float64 `json:"share"`
}

type Run struct {
	ID         int    `json:"id"`
	State      string `json:"state"`
	Superseded bool   `json:"superseded"`
	Error      string `json:"error"`
}

// Item is the tagged union of transcript entries: user, assistant, activity, draft, queued, compaction.
type Item struct {
	Kind      string     `json:"kind"`
	Seq       int        `json:"seq"`
	ID        int        `json:"id"` // queued id
	Text      string     `json:"text"`
	Files     []File     `json:"files"`
	Citations []Citation `json:"citations"`
	RunID     int        `json:"run_id"`
	Steps     []Step     `json:"steps"`
	Draft     *Draft     `json:"draft"`
}

type File struct {
	ID        int    `json:"id"`
	ThreadID  int    `json:"thread_id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Origin    string `json:"origin"`
	Bytes     int64  `json:"bytes"`
}

type Citation struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Cited string `json:"cited"`
}

// Step is the tagged union of activity steps: thinking, note, tool, web_search.
type Step struct {
	Kind          string      `json:"kind"`
	Text          string      `json:"text"`
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Args          string      `json:"args"`
	State         string      `json:"state"`
	IsError       bool        `json:"is_error"`
	Result        string      `json:"result"`
	ChildThreadID int         `json:"child_thread_id"`
	Error         string      `json:"error"`
	Results       []WebResult `json:"results"`
}

type WebResult struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type Draft struct {
	ID      int    `json:"id"`
	Subject string `json:"subject"`
	Status  string `json:"status"`
	ToAddrs string `json:"to_addrs"`
}

type Work struct {
	Plan      *Plan      `json:"plan"`
	Sources   []Source   `json:"sources"`
	Subagents []Subagent `json:"subagents"`
	Files     []File     `json:"files"`
	Drafts    []Draft    `json:"drafts"`
	Code      []CodeRun  `json:"code"`
}

type Plan struct {
	Items []PlanStep `json:"items"`
}

type PlanStep struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

type Source struct {
	Kind  string `json:"kind"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Via   string `json:"via"`
}

type Subagent struct {
	ThreadID int      `json:"thread_id"`
	Role     string   `json:"role"`
	Kind     string   `json:"kind"`
	Effort   string   `json:"effort"`
	Status   string   `json:"status"`
	Current  string   `json:"current"`
	Title    string   `json:"title"`
	Cost     float64  `json:"cost"`
	Elapsed  *float64 `json:"elapsed"`
	Error    string   `json:"error"`
}

type CodeRun struct {
	Tool      string  `json:"tool"`
	StartedAt float64 `json:"started_at"`
	State     string  `json:"state"`
	Script    string  `json:"script"`
	Result    string  `json:"result"`
}

// ---- proposals ----

type ProposalsPageData struct {
	Proposals    []Proposal     `json:"proposals"`
	Status       string         `json:"status"`
	Kind         string         `json:"kind"`
	Counts       map[string]int `json:"counts"`
	PendingCount int            `json:"pending_count"`
	Paused       bool           `json:"paused"`
	Mode         string         `json:"mode"`
	Partial      bool           `json:"partial"`
	PendingOOB   bool           `json:"pending_oob"`
	Toast        *Toast         `json:"toast"`
}

type Proposal struct {
	ID        int         `json:"id"`
	Kind      string      `json:"kind"`
	Status    string      `json:"status"`
	Account   string      `json:"account"`
	Title     string      `json:"title"`
	CreatedTS float64     `json:"created_ts"`
	AppliedTS float64     `json:"applied_ts"`
	DecidedTS float64     `json:"decided_ts"`
	Undoable  bool        `json:"undoable"`
	Result    *PropResult `json:"result"`
	Preview   Preview     `json:"preview"`
}

type PropResult struct {
	Summary string `json:"summary"`
	Error   string `json:"error"`
}

// Preview is the tagged union over proposal kinds (field set = union of all kinds' previews).
type Preview struct {
	// rebalance_split / inbox_backlog / rebalance_merge
	Folder     string     `json:"folder"`
	Groups     []Group    `json:"groups"`
	Sources    []MergeSrc `json:"sources"`
	Into       string     `json:"into"`
	IntoExists bool       `json:"into_exists"`
	Group      *Group     `json:"group"`
	// deletion_rule
	Rule     *Rule     `json:"rule"`
	Reason   string    `json:"reason"`
	Backlog  int       `json:"backlog"`
	Messages []Message `json:"messages"`
	Senders  []Counted `json:"senders"`
	Subjects []string  `json:"subjects"`
	// gmail_label_merge
	From      string `json:"from"`
	FromCount int    `json:"from_count"`
	IntoCount int    `json:"into_count"`
	// notion_link / notion_edit
	A           *PageRef   `json:"a"`
	B           *PageRef   `json:"b"`
	Page        *PageRef   `json:"page"`
	Instruction string     `json:"instruction"`
	Summary     string     `json:"summary"`
	Diff        []DiffLine `json:"diff"`
	// calendar_event
	Calendar string      `json:"calendar"`
	AllDay   bool        `json:"all_day"`
	Start    string      `json:"start"`
	End      string      `json:"end"`
	Location string      `json:"location"`
	Notes    string      `json:"notes"`
	Source   *MailSource `json:"source"`
	// reminder
	Text     string `json:"text"`
	Due      string `json:"due"`
	RemindAt string `json:"remind_at"`
	Subject  string `json:"subject"`
	// notion_wiki_plan
	Counts    map[string]int `json:"counts"`
	Hubs      []Hub          `json:"hubs"`
	Databases []Database     `json:"databases"`
	Items     []WikiItem     `json:"items"`
}

type Group struct {
	Name     string    `json:"name"`
	Count    int       `json:"count"`
	Messages []Message `json:"messages"`
	Terms    []string  `json:"terms"`
	Senders  []Counted `json:"senders"`
	Subjects []string  `json:"subjects"`
}

type MergeSrc struct {
	Folder string `json:"folder"`
	Count  int    `json:"count"`
}

type Counted struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type Message struct {
	ID       int     `json:"id"`
	Subject  string  `json:"subject"`
	FromName string  `json:"from_name"`
	FromAddr string  `json:"from_addr"`
	DateTS   float64 `json:"date_ts"`
}

type Rule struct {
	Account string      `json:"account"`
	When    []Condition `json:"when"`
}

// Condition.Value is string | []string | absent in the source data.
type Condition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

type PageRef struct {
	ID    string      `json:"id"`
	Title string      `json:"title"`
	URL   string      `json:"url"`
	Path  []PathCrumb `json:"path"`
}

type PathCrumb struct {
	Title string `json:"title"`
}

type MailSource struct {
	Subject string `json:"subject"`
}

type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

type Hub struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type Database struct {
	Title  string `json:"title"`
	Mode   string `json:"mode"`
	Hub    string `json:"hub"`
	Reason string `json:"reason"`
}

type WikiItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Decision  string `json:"decision"`
	Source    string `json:"source"`
	Hub       string `json:"hub"`
	MergeInto string `json:"merge_into"`
	Reason    string `json:"reason"`
}
