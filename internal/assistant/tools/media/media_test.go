package mediatools_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/tool"
	mediatools "github.com/jeiang/atrium/internal/assistant/tools/media"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
)

var wantNames = []string{
	"media_add_magnet", "media_channel_add", "media_channel_remove", "media_channels", "media_downloads", "media_library_search",
}

func TestToolsArePythonTools(t *testing.T) {
	var names []string
	for _, tl := range mediatools.Tools() {
		names = append(names, tl.Name)
		registrytest.Verify(t, tl)
	}
	slices.Sort(names)
	if !slices.Equal(names, wantNames) {
		t.Errorf("tools %v, want %v", names, wantNames)
	}
}

func TestEffectRunKindMatrix(t *testing.T) {
	r := registry.New()
	if err := r.Add(mediatools.Tools()...); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range registrytest.Matrix(t) {
		if !slices.Contains(wantNames, e.Name) {
			continue
		}
		seen++
		for _, k := range tool.AllKinds {
			found, err := r.For(k)
			if err != nil {
				t.Fatal(err)
			}
			offered := slices.ContainsFunc(found, func(tl tool.Tool) bool { return tl.Name == e.Name })
			if offered != e.Contexts.Has(k) {
				t.Errorf("%s offered in %s: %v, matrix %v", e.Name, k, offered, e.Contexts.Has(k))
			}
		}
	}
	if seen != len(wantNames) {
		t.Errorf("%d media tools in the matrix, want %d", seen, len(wantNames))
	}
	for _, tl := range mediatools.Tools() {
		wantWrite := slices.Contains([]string{"media_add_magnet", "media_channel_add", "media_channel_remove"}, tl.Name)
		if wantWrite != (tl.Effect == tool.Write) || (!wantWrite && tl.Effect != tool.Read) {
			t.Errorf("%s effect %s", tl.Name, tl.Effect)
		}
		if !wantWrite && tl.Contexts.Has(tool.Nightly) {
			t.Errorf("%s offered to nightly", tl.Name)
		}
	}
}

func dict(t *testing.T, v any) py.Dict {
	t.Helper()
	d, ok := v.(py.Dict)
	if !ok {
		t.Fatalf("%T is not a dict", v)
	}
	return d
}

func TestReadTools(t *testing.T) {
	w := newWorld(t)
	w.completed(hashA, "Lanterns.S01E08.1080p.WEB.h264-ETHEL", []string{"media"})
	w.libraryFiles()
	if _, err := media.AddChannel(t.Context(), w.d.Write, "Lanterns", "tt1234567", "1080p", false, nil, func() float64 { return now }, false); err != nil {
		t.Fatal(err)
	}

	out := w.call("media_downloads", `{}`)
	torrents := list(t, get(t, out, "torrents"))
	if len(torrents) != 1 {
		t.Fatalf("torrents %s", py.JSONDumps(out))
	}
	first := dict(t, torrents[0])
	for key, want := range map[string]any{
		"hash": hashA, "kind": "media", "state": "done", "filed": false, "progress_percent": int64(100), "size_bytes": int64(1000), "eta_seconds": int64(8640000),
	} {
		if got := get(t, first, key); got != want {
			t.Errorf("torrent %s = %v, want %v", key, got, want)
		}
	}
	waiting := dict(t, list(t, get(t, out, "waiting_to_file"))[0])
	if get(t, waiting, "id") != hashA || get(t, waiting, "torrent") != true {
		t.Errorf("waiting %s", py.JSONDumps(waiting))
	}
	if log := w.qb.writeLog(); len(log) != 0 {
		t.Errorf("a read tool wrote to qBittorrent: %v", log)
	}

	found := w.call("media_library_search", `{"query": "dune"}`)
	result := dict(t, list(t, get(t, found, "results"))[0])
	urls := list(t, get(t, result, "vpn_urls"))
	if len(urls) != 1 || urls[0] != "http://artemis.vpn:8084/Dune%20%282021%29/Dune%20%282021%29.mkv" {
		t.Errorf("vpn_urls %v", urls)
	}
	episode := dict(t, list(t, get(t, w.call("media_library_search", `{"query": "lanterns s01e08", "limit": 1}`), "results"))[0])
	if get(t, episode, "kind") != "episode" || list(t, get(t, episode, "vpn_urls"))[0] != "http://artemis.vpn:8085/Lanterns%20%282026%29/Season%2001/Lanterns%20%282026%29%20-%20S01E08.mkv" {
		t.Errorf("episode %s", py.JSONDumps(episode))
	}

	channels := w.call("media_channels", `{}`)
	if name := get(t, dict(t, list(t, get(t, channels, "channels"))[0]), "name"); name != "Lanterns" {
		t.Errorf("channel %v", name)
	}
	if get(t, channels, "last_check") != nil || len(list(t, get(t, channels, "errors"))) != 0 {
		t.Errorf("status %s", py.JSONDumps(channels))
	}

	_, err := w.callWith(w.tc, "media_library_search", `{}`)
	wantToolError(t, err, "query is required")
	bare := w.ctxWith(map[string]string{}, w.tc.ThreadID)
	_, err = w.callWith(bare, "media_downloads", `{}`)
	wantNotConfigured(t, err)
	_, err = w.callWith(bare, "media_library_search", `{"query": "x"}`)
	wantNotConfigured(t, err)
	w.qb.fail = 500
	_, err = w.callWith(w.tc, "media_downloads", `{}`)
	wantToolError(t, err, "qBittorrent: torrents/info: 500")
}

func TestAddMagnetToolAndUndo(t *testing.T) {
	w := newWorld(t)
	w.qb.add(hashC, "Untagged", nil, "downloading", 0.5, "", fakeFile{0, "movie.mkv", 1}, fakeFile{1, "info.nfo", 1})
	w.qb.add(hashD, "Tagged", []string{"media"}, "downloading", 0.5, "", fakeFile{0, "show.mkv", 1}, fakeFile{1, "info.nfo", 1})
	magnet := "magnet:?xt=urn:btih:" + hashA + "&dn=x"

	out := w.call("media_add_magnet", `{"magnet": "`+magnet+`"}`)
	if get(t, out, "hash") != hashA || get(t, out, "kind") != "media" {
		t.Fatalf("out %s", py.JSONDumps(out))
	}
	if last := w.qb.added[len(w.qb.added)-1]; !slices.Equal(last.tags, []string{"media"}) || last.hash != hashA {
		t.Errorf("added %+v", last)
	}
	writeID := get(t, out, "write_id").(int64)
	row, err := writes.Get(t.Context(), w.d.Read, writeID)
	if err != nil || row == nil {
		t.Fatalf("write %v, %v", row, err)
	}
	if row.Tool != "media_add_magnet" || row.Target != hashA || *row.UndoJSON != `{"hash": "`+hashA+`", "op": "media_torrent_delete"}` {
		t.Errorf("ledger row %+v undo %s", row, *row.UndoJSON)
	}

	if !slices.Equal(w.qb.priorities(hashD), []int{1, 0}) || !slices.Contains(w.qb.tagsOf(hashD), "selected") {
		t.Errorf("the tagged torrent was not selected: %v %v", w.qb.priorities(hashD), w.qb.tagsOf(hashD))
	}
	if !slices.Equal(w.qb.priorities(hashC), []int{1, 1}) || len(w.qb.tagsOf(hashC)) != 0 {
		t.Errorf("the untagged torrent was touched: %v %v", w.qb.priorities(hashC), w.qb.tagsOf(hashC))
	}
	for _, line := range w.qb.writeLog() {
		if strings.Contains(line, hashC) {
			t.Errorf("a write targeted the untagged torrent: %s", line)
		}
	}

	if _, err := w.undo(writeID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.qb.deleted, []string{hashA + " true"}) || w.qb.has(hashA) || !w.qb.has(hashC) || !w.qb.has(hashD) {
		t.Errorf("undo deleted %v", w.qb.deleted)
	}
	_, err = w.undo(writeID)
	wantUndoError(t, err, "already undone")

	plain := w.call("media_add_magnet", `{"magnet": "magnet:?xt=urn:btih:`+hashB+`", "media": false}`)
	if get(t, plain, "kind") != "plain" || len(w.qb.added[len(w.qb.added)-1].tags) != 0 {
		t.Errorf("plain add %s %+v", py.JSONDumps(plain), w.qb.added[len(w.qb.added)-1])
	}
	plainID := get(t, plain, "write_id").(int64)
	w.qb.items[hashB].tags = append(w.qb.items[hashB].tags, "filed")
	_, err = w.undo(plainID)
	wantUndoError(t, err, "the torrent was already filed into the library")
	if !w.qb.has(hashB) {
		t.Error("a filed torrent was deleted")
	}
	delete(w.qb.items, hashB)
	w.qb.order = slices.DeleteFunc(w.qb.order, func(h string) bool { return h == hashB })
	_, err = w.undo(plainID)
	wantUndoError(t, err, "the torrent is no longer in qBittorrent")

	for _, bad := range []string{`{"magnet": "http://x"}`, `{"magnet": "magnet:?dn=nohash"}`, `{"magnet": "` + magnet + `", "media": "yes"}`, `{}`} {
		_, err := w.callWith(w.tc, "media_add_magnet", bad)
		wantToolError(t, err, "")
	}
	_, err = w.callWith(w.ctxWith(map[string]string{}, w.tc.ThreadID), "media_add_magnet", `{"magnet": "`+magnet+`"}`)
	wantNotConfigured(t, err)
	w.qb.fail = 500
	_, err = w.callWith(w.tc, "media_add_magnet", `{"magnet": "magnet:?xt=urn:btih:`+hashC+`"}`)
	wantToolError(t, err, "qBittorrent: torrents/add: 500")
}

func TestChannelToolsAndUndo(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	out := w.call("media_channel_add", `{"tmdb_id": 42, "regex": "1080p"}`)
	for key, want := range map[string]any{"name": "Lanterns", "imdb": "1234567", "tmdb_id": int64(42), "case_sensitive": false, "primed": false} {
		if got := get(t, out, key); got != want {
			t.Errorf("channel %s = %v, want %v", key, got, want)
		}
	}
	row, err := writes.Get(ctx, w.d.Read, get(t, out, "write_id").(int64))
	if err != nil || row == nil || row.Tool != "media_channel_add" || row.Target != "channel:1" || row.BeforeJSON != nil ||
		*row.UndoJSON != `{"id": 1, "op": "media_channel_delete"}` {
		t.Fatalf("ledger row %+v, %v", row, err)
	}
	if _, err := w.undo(row.ID); err != nil {
		t.Fatal(err)
	}
	if rows, _ := media.ListChannels(ctx, w.d.Read); len(rows) != 0 {
		t.Errorf("undo left %v", rows)
	}

	byIMDB := w.call("media_channel_add", `{"imdb_id": "tt1234567", "regex": "720p", "case_sensitive": true}`)
	if get(t, byIMDB, "name") != "Lanterns" || get(t, byIMDB, "case_sensitive") != true || get(t, byIMDB, "tmdb_id") != nil {
		t.Errorf("by imdb %s", py.JSONDumps(byIMDB))
	}
	named := w.call("media_channel_add", `{"imdb_id": "7654321", "regex": "x", "name": "Custom"}`)
	if get(t, named, "name") != "Custom" {
		t.Errorf("named %s", py.JSONDumps(named))
	}
	removed := w.call("media_channel_remove", `{"id": `+py.JSONDumps(get(t, byIMDB, "id"))+`}`)
	rows, err := media.ListChannels(ctx, w.d.Read)
	if err != nil || get(t, removed, "removed") != get(t, byIMDB, "id") || len(rows) != 1 || rows[0].ID != get(t, named, "id") {
		t.Fatalf("removed %s, channels %+v, %v", py.JSONDumps(removed), rows, err)
	}
	removal, err := writes.Get(ctx, w.d.Read, get(t, removed, "write_id").(int64))
	if err != nil || removal == nil || removal.Tool != "media_channel_remove" || removal.AfterJSON != nil || removal.BeforeJSON == nil {
		t.Fatalf("removal row %+v, %v", removal, err)
	}
	if _, err := w.undo(removal.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := media.GetChannel(ctx, w.d.Read, get(t, byIMDB, "id").(int64))
	if err != nil || restored == nil || restored.Pattern != "720p" || !restored.CaseSensitive || restored.Created != now || restored.TMDBID != nil {
		t.Fatalf("restored %+v, %v", restored, err)
	}
	_, err = w.undo(removal.ID)
	wantUndoError(t, err, "already undone")

	for _, bad := range []string{
		`{"regex": "x"}`, `{"imdb_id": "bad", "regex": "x"}`, `{"tmdb_id": 43, "regex": "x"}`, `{"tmdb_id": "42", "regex": "x"}`,
		`{"tmdb_id": true, "regex": "x"}`, `{"imdb_id": "tt1234567", "regex": "("}`, `{"imdb_id": "tt1234567"}`,
		`{"imdb_id": "tt1234567", "regex": "x", "case_sensitive": "no"}`,
	} {
		_, err := w.callWith(w.tc, "media_channel_add", bad)
		wantToolError(t, err, "")
	}
	_, err = w.callWith(w.tc, "media_channel_add", `{"tmdb_id": 43, "regex": "x"}`)
	wantToolError(t, err, "TMDB show 43 has no IMDb id")
	for _, bad := range []string{`{"id": "x"}`, `{"id": 999}`, `{}`} {
		_, err := w.callWith(w.tc, "media_channel_remove", bad)
		wantToolError(t, err, "")
	}
	_, err = w.callWith(w.tc, "media_channel_remove", `{"id": 999}`)
	wantToolError(t, err, "channel 999 does not exist")

	noTMDB := w.ctxWithout("TMDB_API_KEY")
	_, err = w.callWith(noTMDB, "media_channel_add", `{"tmdb_id": 42, "regex": "x"}`)
	wantNotConfigured(t, err)
	bare, err := w.callWith(noTMDB, "media_channel_add", `{"imdb_id": "tt1111111", "regex": "x"}`)
	if err != nil || get(t, bare, "name") != "tt1111111" {
		t.Errorf("without TMDB: %v, %v", bare, err)
	}
}
