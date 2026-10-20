package rebalance

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/llm"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/scanutil"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

var log = logx.Logger("atrium.rebalance")

const (
	Split              = "rebalance_split"
	Merge              = "rebalance_merge"
	MinFolder          = 40
	MinGroup           = 20
	MinShare           = 0.10
	MergeMinGroup      = 3
	MergeMinOthers     = 10
	MergeMinShare      = 0.9
	RunsRequired       = 2
	RunGapSeconds      = 14 * 86400
	ClusterIterations  = 15
	ClusterMargin      = 0.04
	ClusterMinEmbedded = 0.8
	TopTerms           = 5
	NameMaxChars       = 40
)

var ClusterKs = [...]int{2, 3, 4}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields("the and for you your with from this that have has are was our new now not but out all can get " +
		"will just more one any its off per via") {
		stopwords[w] = true
	}
}

const NameSystem = "You name a mail folder. You get the existing parent folder, the top sender domains and common subject words " +
	"of a group of messages that will move into a new folder. Answer a short, plain folder name of one to three " +
	"words that describes what the messages are, without the sender's name unless that is what they are about."

var NameSchema = llm.Object{
	{Key: "type", Value: "object"},
	{Key: "properties", Value: llm.Object{{Key: "name", Value: llm.Object{{Key: "type", Value: "string"}, {Key: "maxLength", Value: NameMaxChars}}}}},
	{Key: "required", Value: []string{"name"}},
	{Key: "additionalProperties", Value: false},
}

var (
	termRE   = rx.MustCompile(`[a-z]{3,}`, 0)
	junkRE   = rx.MustCompile(`[/|\\\x00-\x1f]+`, 0)
	spacesRE = rx.MustCompile(`\s+`, 0)
)

type row struct {
	scanutil.Message
	Account     string
	Folder      string
	FromDomain  string
	SubjectTmpl string
}

type keyer func(r *row) string

func domainKey(r *row) string { return r.FromDomain }

func addrKey(r *row) string { return r.FromAddr }

func tmplKey(r *row) string {
	if r.FromAddr != "" && r.SubjectTmpl != "" {
		return r.FromAddr + "|" + r.SubjectTmpl
	}
	return ""
}

type level struct {
	name string
	key  keyer
}

var (
	levels      = []level{{"domain", domainKey}, {"addr", addrKey}, {"tmpl", tmplKey}}
	mergeLevels = []level{{"addr", addrKey}, {"tmpl", tmplKey}}
)

type Group struct {
	Key string
	IDs []int64
}

type Candidate struct {
	Kind    string
	Account string
	Folder  string
	Level   string
	Groups  []Group
	Into    string
}

func (c Candidate) Fingerprint() string {
	keys := make([]string, len(c.Groups))
	for i, g := range c.Groups {
		keys[i] = g.Key
	}
	slices.Sort(keys)
	return fmt.Sprintf("%s:%s:%s:%s:%s:%s", c.Kind, c.Account, c.Folder, c.Into, c.Level, scanutil.Digest(strings.Join(keys, "\x01")))
}

type folderKey struct{ account, folder string }

type folders struct {
	order []folderKey
	rows  map[folderKey][]*row
}

func (f folders) sorted() []folderKey {
	keys := slices.Clone(f.order)
	slices.SortFunc(keys, func(a, b folderKey) int {
		return cmp.Or(cmp.Compare(a.account, b.account), cmp.Compare(a.folder, b.folder))
	})
	return keys
}

func folderRows(ctx context.Context, q proposals.Querier) (folders, error) {
	f := folders{rows: map[folderKey][]*row{}}
	rows, err := q.QueryContext(ctx, "SELECT id, account, folder, from_addr, from_domain, from_name, subject, subject_tmpl, date_ts "+
		"FROM messages WHERE gone_at IS NULL AND sent = 0 AND in_inbox = 0 AND folder IS NOT NULL AND folder != '' ORDER BY id")
	if err != nil {
		return f, err
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (*row, error) {
		var m row
		err := r.Scan(&m.ID, &m.Account, &m.Folder, &m.FromAddr, &m.FromDomain, &m.FromName, &m.Subject, &m.SubjectTmpl, &m.DateTS)
		return &m, err
	})
	if err != nil {
		return f, err
	}
	for _, m := range all {
		key := folderKey{m.Account, m.Folder}
		if _, seen := f.rows[key]; !seen {
			f.order = append(f.order, key)
		}
		f.rows[key] = append(f.rows[key], m)
	}
	return f, nil
}

type grouped struct {
	order []string
	ids   map[string][]int64
}

func groupBy(rows []*row, key keyer) grouped {
	g := grouped{ids: map[string][]int64{}}
	for _, r := range rows {
		k := key(r)
		if k == "" {
			continue
		}
		if _, seen := g.ids[k]; !seen {
			g.order = append(g.order, k)
		}
		g.ids[k] = append(g.ids[k], r.ID)
	}
	return g
}

func big(size, total int) bool {
	return size >= MinGroup && float64(size) >= MinShare*float64(total)
}

func sortGroups(groups []Group) {
	slices.SortStableFunc(groups, func(a, b Group) int {
		return cmp.Or(cmp.Compare(len(b.IDs), len(a.IDs)), cmp.Compare(a.Key, b.Key))
	})
}

func bigGroups(rows []*row, key keyer, total int) []Group {
	g := groupBy(rows, key)
	var out []Group
	for _, k := range g.order {
		if big(len(g.ids[k]), total) {
			out = append(out, Group{k, g.ids[k]})
		}
	}
	sortGroups(out)
	return out
}

func senderSplit(rows []*row) (string, []Group, bool) {
	for _, lv := range levels {
		groups := bigGroups(rows, lv.key, len(rows))
		if len(groups) >= 2 {
			return lv.name, groups[1:], true
		}
	}
	return "", nil, false
}

func unitVectors(ctx context.Context, q proposals.Querier, ids []int64) (map[int64][]float32, error) {
	found := map[int64][]float32{}
	for _, chunk := range mailops.Chunks(ids, 500) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := q.QueryContext(ctx, "SELECT rowid, embedding FROM message_vec WHERE rowid IN (?"+strings.Repeat(",?", len(chunk)-1)+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var blob []byte
			if err := rows.Scan(&id, &blob); err != nil {
				_ = rows.Close()
				return nil, err
			}
			v := make([]float32, len(blob)/4)
			for i := range v {
				v[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[4*i:]))
			}
			found[id] = v
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return found, nil
}

type clusterer struct{ terms []float64 }

func (c *clusterer) dot(a, b []float32) float64 {
	c.terms = c.terms[:0]
	for i := range a {
		c.terms = append(c.terms, float64(a[i])*float64(b[i]))
	}
	return py.Sum(c.terms)
}

func (c *clusterer) centroid(members [][]float32) []float32 {
	total := make([]float64, len(members[0]))
	for i := range total {
		c.terms = c.terms[:0]
		for _, m := range members {
			c.terms = append(c.terms, float64(m[i]))
		}
		total[i] = py.Sum(c.terms)
	}
	norm := math.Sqrt(py.SumSq(total))
	if norm == 0 {
		norm = 1.0
	}
	out := make([]float32, len(total))
	for i, x := range total {
		out[i] = float32(x / norm)
	}
	return out
}

func argmin(n int, value func(i int) float64) int {
	best, low := 0, value(0)
	for i := 1; i < n; i++ {
		if v := value(i); v < low {
			best, low = i, v
		}
	}
	return best
}

func maxOf(values []float64) int {
	best := 0
	for i, v := range values {
		if v > values[best] {
			best = i
		}
	}
	return best
}

func (c *clusterer) kmeans(vectors [][]float32, k int) ([]int, float64) {
	mean := c.centroid(vectors)
	centers := [][]float32{vectors[argmin(len(vectors), func(i int) float64 { return c.dot(vectors[i], mean) })]}
	for len(centers) < k {
		pick := argmin(len(vectors), func(i int) float64 {
			sims := make([]float64, len(centers))
			for j, ctr := range centers {
				sims[j] = c.dot(vectors[i], ctr)
			}
			return sims[maxOf(sims)]
		})
		centers = append(centers, vectors[pick])
	}
	var assign []int
	var sims [][]float64
	for range ClusterIterations {
		sims = make([][]float64, len(vectors))
		fresh := make([]int, len(vectors))
		for i, v := range vectors {
			sims[i] = make([]float64, k)
			for j, ctr := range centers {
				sims[i][j] = c.dot(v, ctr)
			}
			fresh[i] = maxOf(sims[i])
		}
		if slices.Equal(fresh, assign) {
			break
		}
		assign = fresh
		for j := range k {
			var members [][]float32
			for i, v := range vectors {
				if assign[i] == j {
					members = append(members, v)
				}
			}
			if len(members) > 0 {
				centers[j] = c.centroid(members)
			}
		}
	}
	margin := 0.0
	for i, s := range sims {
		a := assign[i]
		next, found := 0.0, false
		for j, x := range s {
			if j != a && (!found || x > next) {
				next, found = x, true
			}
		}
		margin += s[a] - next
	}
	return assign, margin / float64(len(vectors))
}

func embeddingSplit(ctx context.Context, q proposals.Querier, rows []*row) (string, []Group, bool, error) {
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	vectors, err := unitVectors(ctx, q, ids)
	if err != nil {
		return "", nil, false, err
	}
	if float64(len(vectors)) < ClusterMinEmbedded*float64(len(rows)) {
		return "", nil, false, nil
	}
	var order []int64
	var points [][]float32
	for _, id := range ids {
		if v, ok := vectors[id]; ok {
			order = append(order, id)
			points = append(points, v)
		}
	}
	var c clusterer
	var assign []int
	margin := 0.0
	for i, k := range ClusterKs {
		a, m := c.kmeans(points, k)
		if i == 0 || m > margin {
			assign, margin = a, m
		}
	}
	if margin < ClusterMargin {
		return "", nil, false, nil
	}
	var labels []int
	clusters := map[int][]int64{}
	for i, id := range order {
		a := assign[i]
		if _, seen := clusters[a]; !seen {
			labels = append(labels, a)
		}
		clusters[a] = append(clusters[a], id)
	}
	byID := map[int64]*row{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	var groups []Group
	for _, a := range labels {
		members := clusters[a]
		if !big(len(members), len(rows)) {
			continue
		}
		domains := make([]string, len(members))
		for i, id := range members {
			domains[i] = byID[id].FromDomain
		}
		top := scanutil.TopCounts(domains)
		names := make([]string, 0, 3)
		for _, d := range top[:min(len(top), 3)] {
			label, _ := d.(py.Dict).Get("label")
			names = append(names, label.(string))
		}
		slices.Sort(names)
		groups = append(groups, Group{strings.Join(names, "|"), members})
	}
	sortGroups(groups)
	if len(groups) < 2 {
		return "", nil, false, nil
	}
	return "embed", groups[1:], true, nil
}

func splitCandidates(ctx context.Context, q proposals.Querier, f folders) ([]Candidate, error) {
	var found []Candidate
	for _, key := range f.sorted() {
		rows := f.rows[key]
		if len(rows) < MinFolder {
			continue
		}
		lv, groups, ok := senderSplit(rows)
		if !ok {
			var err error
			if lv, groups, ok, err = embeddingSplit(ctx, q, rows); err != nil {
				return nil, err
			}
		}
		if ok {
			found = append(found, Candidate{Kind: Split, Account: key.account, Folder: key.folder, Level: lv, Groups: groups})
		}
	}
	return found, nil
}

type placedKey struct{ account, key string }

type counter struct {
	order []string
	n     map[string]int
}

func mergeCandidates(f folders) []Candidate {
	var found []Candidate
	for _, lv := range mergeLevels {
		placed := map[placedKey]*counter{}
		for _, fk := range f.order {
			for _, r := range f.rows[fk] {
				k := lv.key(r)
				if k == "" {
					continue
				}
				pk := placedKey{fk.account, k}
				c := placed[pk]
				if c == nil {
					c = &counter{n: map[string]int{}}
					placed[pk] = c
				}
				if c.n[fk.folder] == 0 {
					c.order = append(c.order, fk.folder)
				}
				c.n[fk.folder]++
			}
		}
		for _, fk := range f.sorted() {
			rows := f.rows[fk]
			if len(rows) < MinFolder {
				continue
			}
			g := groupBy(rows, lv.key)
			keys := slices.Clone(g.order)
			slices.Sort(keys)
			for _, k := range keys {
				ids := g.ids[k]
				counts := placed[placedKey{fk.account, k}]
				total, into, top := 0, "", -1
				for _, folder := range counts.order {
					if folder == fk.folder {
						continue
					}
					n := counts.n[folder]
					total += n
					if n > top || n == top && folder > into {
						into, top = folder, n
					}
				}
				if len(ids) < MergeMinGroup || total < MergeMinOthers {
					continue
				}
				if float64(top)/float64(total) >= MergeMinShare {
					found = append(found, Candidate{Kind: Merge, Account: fk.account, Folder: fk.folder, Level: lv.name,
						Groups: []Group{{k, ids}}, Into: into})
				}
			}
		}
	}
	claimed := map[int64]bool{}
	var unique []Candidate
	for _, c := range found {
		overlap := false
		for _, g := range c.Groups {
			for _, id := range g.IDs {
				overlap = overlap || claimed[id]
			}
		}
		if overlap {
			continue
		}
		for _, g := range c.Groups {
			for _, id := range g.IDs {
				claimed[id] = true
			}
		}
		unique = append(unique, c)
	}
	return unique
}

func Detect(ctx context.Context, q proposals.Querier) ([]Candidate, error) {
	_, found, err := detect(ctx, q)
	return found, err
}

func detect(ctx context.Context, q proposals.Querier) (folders, []Candidate, error) {
	f, err := folderRows(ctx, q)
	if err != nil {
		return f, nil, err
	}
	splits, err := splitCandidates(ctx, q, f)
	if err != nil {
		return f, nil, err
	}
	return f, append(mergeCandidates(f), splits...), nil
}

func termsOf(rows []*row) []string {
	counts := map[string]int{}
	var words []string
	for _, r := range rows {
		found, _ := termRE.FindAll(r.SubjectTmpl)
		seen := map[string]bool{}
		for _, w := range found {
			if stopwords[w] || seen[w] {
				continue
			}
			seen[w] = true
			if counts[w] == 0 {
				words = append(words, w)
			}
			counts[w]++
		}
	}
	slices.SortFunc(words, func(a, b string) int { return cmp.Or(cmp.Compare(counts[b], counts[a]), cmp.Compare(a, b)) })
	return words[:min(len(words), TopTerms)]
}

func CleanName(value string) string {
	name, _ := junkRE.Sub(" ", value, 0)
	name, _ = spacesRE.Sub(" ", name, 0)
	return py.Strip(py.Clip(py.Strip(name), NameMaxChars))
}

type Namer func(ctx context.Context, folder string, domains, terms []string) (string, error)

func SuggestName(chatURL string) Namer {
	return func(ctx context.Context, folder string, domains, terms []string) (string, error) {
		prompt := fmt.Sprintf("Parent folder: %s\nTop sender domains: %s\nCommon subject words: %s", folder,
			strings.Join(domains, ", "), strings.Join(terms, ", "))
		name := ""
		raw, err := llm.ChatJSON(ctx, chatURL, NameSystem, prompt, NameSchema, 0)
		var down *httpx.ServiceUnavailable
		switch {
		case errors.As(err, &down) || errors.Is(err, httpx.ErrValue) || errors.Is(err, httpx.ErrKey) ||
			errors.Is(err, httpx.ErrIndex) || errors.Is(err, httpx.ErrType):
		case err != nil:
			return "", err
		default:
			answer, _ := py.JSONLoads(raw)
			if fields, ok := answer.(py.Dict); ok {
				if value, _ := fields.Get("name"); value != nil {
					if s, isString := value.(string); isString {
						name = CleanName(s)
					}
				}
			}
		}
		if name == "" && len(domains) > 0 {
			name = CleanName(domains[0])
		}
		if name == "" {
			name = "Split"
		}
		return name, nil
	}
}

func groupPreview(rows []*row, name string) py.Dict {
	domains := make([]string, len(rows))
	addrs := make([]string, len(rows))
	messages := make([]scanutil.Message, len(rows))
	views := make([]any, len(rows))
	for i, r := range rows {
		domains[i] = r.FromDomain
		addrs[i] = r.FromAddr
		messages[i] = r.Message
		views[i] = r.View()
	}
	return py.Dict{
		{Key: "name", Value: name}, {Key: "count", Value: len(rows)}, {Key: "senders", Value: scanutil.TopCounts(domains)},
		{Key: "addresses", Value: scanutil.TopCounts(addrs)}, {Key: "terms", Value: termsOf(rows)},
		{Key: "subjects", Value: scanutil.SampleSubjectsOf(messages)}, {Key: "messages", Value: views},
	}
}

type built struct {
	title            string
	payload, preview py.Dict
}

func snapshots(ctx context.Context, q proposals.Querier, ids []int64) ([]any, error) {
	snaps, err := mailops.Snapshots(ctx, q, ids)
	return scanutil.SnapshotList(snaps), err
}

func groupRows(byID map[int64]*row, ids []int64) []*row {
	rows := make([]*row, 0, len(ids))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			rows = append(rows, r)
		}
	}
	slices.SortFunc(rows, func(a, b *row) int { return cmp.Compare(a.ID, b.ID) })
	return rows
}

func build(ctx context.Context, q proposals.Querier, byID map[int64]*row, cand Candidate, namer Namer) (built, error) {
	if cand.Kind == Merge {
		g := cand.Groups[0]
		rows := groupRows(byID, g.IDs)
		snaps, err := snapshots(ctx, q, g.IDs)
		if err != nil {
			return built{}, err
		}
		return built{
			title: fmt.Sprintf("Move %d messages from %s to %s", len(rows), cand.Folder, cand.Into),
			payload: py.Dict{{Key: "folder", Value: cand.Folder}, {Key: "groups", Value: []any{
				py.Dict{{Key: "dest", Value: cand.Into}, {Key: "messages", Value: snaps}},
			}}},
			preview: py.Dict{
				{Key: "folder", Value: cand.Folder},
				{Key: "sources", Value: []any{py.Dict{{Key: "folder", Value: cand.Folder}, {Key: "count", Value: len(rows)}}}},
				{Key: "into", Value: cand.Into}, {Key: "into_exists", Value: true}, {Key: "group", Value: groupPreview(rows, cand.Into)},
			},
		}, nil
	}
	var payloadGroups, previewGroups []any
	count := 0
	for _, g := range cand.Groups {
		rows := groupRows(byID, g.IDs)
		info := groupPreview(rows, "")
		var domains []string
		senders, _ := info.Get("senders")
		for _, d := range senders.([]any) {
			label, _ := d.(py.Dict).Get("label")
			domains = append(domains, label.(string))
		}
		terms, _ := info.Get("terms")
		short, err := namer(ctx, cand.Folder, domains, terms.([]string))
		if err != nil {
			return built{}, err
		}
		dest := cand.Folder + "/" + short
		previewGroups = append(previewGroups, info.Set("name", dest))
		snaps, err := snapshots(ctx, q, g.IDs)
		if err != nil {
			return built{}, err
		}
		payloadGroups = append(payloadGroups, py.Dict{{Key: "dest", Value: dest}, {Key: "messages", Value: snaps}})
		count += len(rows)
	}
	return built{
		title:   fmt.Sprintf("Split %d messages out of %s into %d new folders", count, cand.Folder, len(previewGroups)),
		payload: py.Dict{{Key: "folder", Value: cand.Folder}, {Key: "groups", Value: payloadGroups}},
		preview: py.Dict{{Key: "folder", Value: cand.Folder}, {Key: "groups", Value: previewGroups}},
	}, nil
}

func track(ctx context.Context, d *db.DB, candidates []Candidate, ts float64) (map[string]int64, error) {
	var order []string
	live := map[string]Candidate{}
	for _, c := range candidates {
		fp := c.Fingerprint()
		if _, seen := live[fp]; !seen {
			order = append(order, fp)
		}
		live[fp] = c
	}
	runs := map[string]int64{}
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT fingerprint, last_ts FROM rebalance_candidates")
		if err != nil {
			return err
		}
		type seen struct {
			fp   string
			last float64
		}
		known, err := db.Collect(rows, func(r *sql.Rows) (seen, error) {
			var s seen
			err := r.Scan(&s.fp, &s.last)
			return s, err
		})
		if err != nil {
			return err
		}
		last := map[string]float64{}
		for _, s := range known {
			last[s.fp] = s.last
			if _, ok := live[s.fp]; !ok {
				if _, err := tx.ExecContext(ctx, "DELETE FROM rebalance_candidates WHERE fingerprint = ?", s.fp); err != nil {
					return err
				}
			}
		}
		for _, fp := range order {
			c := live[fp]
			prev, ok := last[fp]
			switch {
			case !ok:
				_, err = tx.ExecContext(ctx, "INSERT INTO rebalance_candidates (fingerprint, kind, account, folder, first_ts, last_ts, runs) "+
					"VALUES (?, ?, ?, ?, ?, ?, 1)", fp, c.Kind, c.Account, c.Folder, ts, ts)
			case ts-prev >= RunGapSeconds:
				_, err = tx.ExecContext(ctx, "UPDATE rebalance_candidates SET runs = runs + 1, last_ts = ? WHERE fingerprint = ?", ts, fp)
			}
			if err != nil {
				return err
			}
		}
		rows, err = tx.QueryContext(ctx, "SELECT fingerprint, runs FROM rebalance_candidates")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var fp string
			var n int64
			if err := rows.Scan(&fp, &n); err != nil {
				return err
			}
			runs[fp] = n
		}
		return rows.Err()
	})
	return runs, err
}

func Scan(ctx context.Context, d *db.DB, ts float64, namer Namer) (scanutil.Result, error) {
	f, candidates, err := detect(ctx, d.Read)
	if err != nil {
		return scanutil.Result{}, err
	}
	runs, err := track(ctx, d, candidates, ts)
	if err != nil {
		return scanutil.Result{}, err
	}
	byID := map[int64]*row{}
	for _, rows := range f.rows {
		for _, r := range rows {
			byID[r.ID] = r
		}
	}
	created := 0
	for _, cand := range candidates {
		fp := cand.Fingerprint()
		if runs[fp] < RunsRequired {
			continue
		}
		known, err := proposals.Known(ctx, d.Write, fp)
		if err != nil {
			return scanutil.Result{}, err
		}
		if known {
			continue
		}
		b, err := build(ctx, d.Read, byID, cand, namer)
		if err != nil {
			return scanutil.Result{}, err
		}
		account := cand.Account
		_, ok, err := proposals.Create(ctx, d, cand.Kind, &account, b.title, b.payload, b.preview, fp, ts)
		if err != nil {
			return scanutil.Result{}, err
		}
		if ok {
			created++
		}
	}
	log.Info(fmt.Sprintf("rebalance: %d candidates, %d new proposals", len(candidates), created))
	return scanutil.Result{Candidates: len(candidates), Created: created}, nil
}
