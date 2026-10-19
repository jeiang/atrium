package media

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // the BitTorrent v1 info hash is SHA-1
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccc"
	now   = 1_800_000_000.0
)

type corpusRecord struct {
	Fn     string          `json:"fn"`
	Input  []any           `json:"input"`
	Output json.RawMessage `json:"output"`
	Error  *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func TestParseCorpus(t *testing.T) {
	f, err := os.Open(filepath.Join(rxtest.Dir(), "media.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	count := 0
	for sc.Scan() {
		var r corpusRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		count++
		str := func(i int) string { s, _ := r.Input[i].(string); return s }
		num := func(i int) int { n, _ := r.Input[i].(float64); return int(n) }
		var got any
		var gotErr error
		switch r.Fn {
		case "parse_release":
			p := ParseRelease(str(0))
			got = map[string]any{"title": p.Title, "year": p.Year, "season": p.Season, "episode": p.Episode}
		case "ext":
			got = Ext(str(0))
		case "is_video":
			got = IsVideo(str(0))
		case "is_subtitle":
			got = IsSubtitle(str(0))
		case "clean_component":
			got, gotErr = cleanComponent(str(0))
		case "named":
			got, gotErr = named(str(0), str(1))
		case "movie_path":
			got, gotErr = MoviePath(str(0), str(1), str(2))
		case "episode_path":
			got, gotErr = EpisodePath(str(0), str(1), num(2), num(3), str(4))
		default:
			t.Fatalf("unknown function %q", r.Fn)
		}
		if r.Error != nil {
			var pe ParseError
			if !errors.As(gotErr, &pe) || string(pe) != r.Error.Message {
				t.Errorf("%s%v: error %v, want ParseError %q", r.Fn, r.Input, gotErr, r.Error.Message)
			}
			continue
		}
		if gotErr != nil {
			t.Errorf("%s%v: %v", r.Fn, r.Input, gotErr)
			continue
		}
		var want any
		if err := json.Unmarshal(r.Output, &want); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(got)
		var gotValue any
		_ = json.Unmarshal(raw, &gotValue)
		if !reflect.DeepEqual(want, gotValue) {
			t.Errorf("%s%v = %s, want %s", r.Fn, r.Input, raw, r.Output)
		}
	}
	if count == 0 {
		t.Fatal("empty corpus")
	}
}

func TestPatterns(t *testing.T) {
	patterns := map[string]*rx.Regexp{
		"src/atrium/media/parse.py:EPISODE_RE":     episodeRe,
		"src/atrium/media/parse.py:YEAR_RE":        yearRe,
		"src/atrium/media/parse.py:LEAD_TAG_RE":    leadTagRe,
		"src/atrium/media/parse.py:SAMPLE_RE":      sampleRe,
		"src/atrium/media/eztv.py:IMDB_RE":         imdbRe,
		"src/atrium/media/library.py:SEASON_DIR":   seasonDir,
		"src/atrium/media/library.py:EPISODE_MARK": episodeMark,
		"src/atrium/media/qbit.py:BTIH":            btih,
	}
	corpus := rxtest.Load(t)
	for _, p := range corpus.Patterns {
		if !strings.HasPrefix(p.ID, "src/atrium/media/channels.py:compile_pattern:") {
			continue
		}
		flags, err := p.Flags()
		if err != nil {
			t.Fatal(err)
		}
		re, err := rx.Compile(p.Pattern, flags)
		if p.Error != "" {
			if err == nil {
				t.Errorf("%s: %q compiled, Python rejects it", p.ID, p.Pattern)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", p.ID, err)
			continue
		}
		patterns[p.ID] = re
	}
	rxtest.Run(t, patterns)
}

func torrentBytes() ([]byte, string) {
	name := "Show.S01E01.1080p"
	info := []byte("d4:name" + fmt.Sprintf("%d:%s", len(name), name) + "12:piece lengthi16384e6:pieces20:" + strings.Repeat("p", 20) + "e")
	sum := sha1.Sum(info) //nolint:gosec // the BitTorrent v1 info hash is SHA-1
	return append(append([]byte("d8:announce9:http://x/4:info"), info...), 'e'), hex.EncodeToString(sum[:])
}

func TestQbitClientRequestsGolden(t *testing.T) {
	srv := golden.Replay(t, golden.LoadScenario(t, "qbittorrent", "client_requests").Exchanges)
	ctx := context.Background()
	qb := NewQbit(srv.URL + "/")
	magnet := "magnet:?xt=urn:btih:" + hashC + "&dn=Lanterns.S01E08"
	data, _ := torrentBytes()
	steps := []func() error{
		func() error { _, err := qb.Torrents(ctx, "", "", ""); return err },
		func() error { _, err := qb.Completed(ctx); return err },
		func() error { _, err := qb.Torrents(ctx, "", TagMedia, ""); return err },
		func() error { _, err := qb.Torrent(ctx, hashA); return err },
		func() error { _, err := qb.Files(ctx, hashA); return err },
		func() error { return qb.AddMagnet(ctx, magnet, TagMedia) },
		func() error { return qb.AddMagnet(ctx, "magnet:?xt=urn:btih:"+strings.Repeat("d", 40), "") },
		func() error { return qb.AddFile(ctx, data, "Show.S01E01.1080p.torrent", TagMedia) },
		func() error { return qb.AddFile(ctx, data, "plain.torrent", "") },
		func() error { return qb.AddTags(ctx, hashB, TagFiled) },
		func() error { return qb.SetPriority(ctx, hashA, []int{1}, PrioritySkip) },
		func() error { return qb.SetPriority(ctx, hashA, []int{0, 1}, PriorityNormal) },
		func() error { return qb.Start(ctx, hashA) },
		func() error { return qb.Delete(ctx, hashB, true) },
		func() error { return qb.Delete(ctx, hashA, false) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
}

func TestQbitAddAnswersGolden(t *testing.T) {
	srv := golden.Replay(t, golden.LoadScenario(t, "qbittorrent", "add_answers").Exchanges)
	ctx := context.Background()
	qb := NewQbit(srv.URL)
	magnet := "magnet:?xt=urn:btih:" + hashC + "&dn=Lanterns.S01E08"
	if err := qb.AddMagnet(ctx, magnet, ""); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var qbitErr QbitError
		if err := qb.AddMagnet(ctx, magnet, ""); !errors.As(err, &qbitErr) {
			t.Errorf("a rejected add returned %v", err)
		}
	}
}

func TestEztvGolden(t *testing.T) {
	ctx := context.Background()
	previous := EZTVAPI
	t.Cleanup(func() { EZTVAPI = previous })

	srv := golden.Replay(t, golden.LoadScenario(t, "eztv", "fetch").Exchanges)
	EZTVAPI = srv.URL + "/api/get-torrents"
	found, err := FetchReleases(ctx, "1234567", eztvLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Hash != hashA || found[1].Hash != hashB || found[1].SizeBytes != 1_200_000_000 {
		t.Errorf("releases = %+v", found)
	}
	if _, err := FetchReleases(ctx, "7654321", 20); err != nil {
		t.Fatal(err)
	}

	srv = golden.Replay(t, golden.LoadScenario(t, "eztv", "errors").Exchanges)
	EZTVAPI = srv.URL + "/api/get-torrents"
	for _, want := range []string{"EZTV: 503", "EZTV: not JSON"} {
		var eztvErr EztvError
		if _, err := FetchReleases(ctx, "1234567", eztvLimit); !errors.As(err, &eztvErr) || err.Error() != want {
			t.Errorf("error = %v, want %q", err, want)
		}
	}
}

func TestChannelPollGolden(t *testing.T) {
	srv := golden.Replay(t, golden.LoadScenario(t, "qbittorrent", "channel_poll").Exchanges)
	previous := EZTVAPI
	EZTVAPI = srv.URL + "/api/get-torrents"
	t.Cleanup(func() { EZTVAPI = previous })
	env := config.Map(map[string]string{
		"QBITTORRENT_URL": srv.URL, "ATRIUM_PUBLIC_URL": "https://atrium.test", "NTFY_URL": srv.URL,
		"NTFY_TOKEN": "golden-ntfy-token", "NTFY_MEDIA_TOPIC": "media-releases",
	})
	settings := config.Media(env)
	d := dbtest.Schema(t)
	ctx := context.Background()
	if _, err := AddChannel(ctx, d.Write, "Lanterns", "tt1234567", `^Lanterns S\d+E\d+ 1080p`, false, nil, func() float64 { return now }, false); err != nil {
		t.Fatal(err)
	}
	qb := NewQbit(settings.QbitURL)
	out, err := Poll(ctx, d.Write, env, qb, settings, PollOptions{Now: func() float64 { return now }})
	if err != nil || len(out.Added) != 0 {
		t.Fatalf("priming poll = %+v, %v", out, err)
	}
	out, err = Poll(ctx, d.Write, env, qb, settings, PollOptions{Now: func() float64 { return now + 60 }})
	if err != nil || len(out.Added) != 1 || out.Added[0].Hash != hashC || len(out.Errors) != 0 {
		t.Fatalf("second poll = %+v, %v", out, err)
	}
}
