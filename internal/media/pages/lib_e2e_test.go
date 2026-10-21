package pages

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func libWrite(t *testing.T, root, rel string, size int) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func libTree(t *testing.T, root string) {
	t.Helper()
	libWrite(t, root, "movies/Dune (2021)/Dune (2021).mkv", 100)
	libWrite(t, root, "tv/Lanterns (2026)/Season 01/Lanterns (2026) - S01E08.mkv", 13)
	libWrite(t, root, "tv/Lanterns (2026)/Season 00/Lanterns (2026) - S00E00.mkv", 7)
}

func libVars(t *testing.T, extra map[string]string) (map[string]string, string) {
	t.Helper()
	root := t.TempDir()
	vars := map[string]string{
		"ATRIUM_MEDIA_MEDIA_ROOT": root, "ATRIUM_CORE_PUBLIC_URL": "https://atrium.test", "ATRIUM_MEDIA_MEDIA_MOVIES_URL": "http://artemis.vpn:8084",
		"ATRIUM_MEDIA_MEDIA_TV_URL": "http://artemis.vpn:8085", "ATRIUM_MEDIA_LINK_KEY": "secret-key",
	}
	for k, v := range extra {
		vars[k] = v
	}
	return vars, root
}

func libHref(t *testing.T, body, prefix string) string {
	t.Helper()
	i := strings.Index(body, `href="`+prefix)
	if i < 0 {
		t.Fatalf("no link starting %q:\n%s", prefix, body)
	}
	rest := body[i+len(`href="`):]
	return strings.ReplaceAll(rest[:strings.Index(rest, `"`)], "&amp;", "&")
}

func TestLibE2EPagesLinksAndMissing(t *testing.T) {
	vars, root := libVars(t, nil)
	libTree(t, root)
	s := newSite(t, vars, Options{})

	index := status(t, s.get("/media/library"), 200, "library")
	contains(t, index,
		"Movies (1)", "TV (1)", "Dune", "Lanterns",
		`href="/media/library/movie/Dune%20%282021%29"`, `src="/media/art/movie/Dune%20%282021%29"`,
		`href="/media/library/tv/Lanterns%20%282026%29"`, `src="/media/art/show/Lanterns%20%282026%29"`,
		`<ul class="grid grid-cols-2`, `aria-label="Movies"`, `aria-label="Shows"`)

	movie := status(t, s.get("/media/library/movie/Dune%20%282021%29"), 200, "movie")
	contains(t, movie,
		"Media · Dune (2021)", "Dune (2021).mkv", "0.0 GB", "Public link", "VPN link", "expire after 12 hours",
		`href="http://artemis.vpn:8084/Dune%20%282021%29/Dune%20%282021%29.mkv"`, `src="/media/art/movie/Dune%20%282021%29"`)
	public := libHref(t, movie, "https://atrium.test/media/file/")
	if !strings.Contains(public, "movies/Dune%20%282021%29/Dune%20%282021%29.mkv?exp=") || !strings.Contains(public, "&sig=") {
		t.Fatalf("public link = %q", public)
	}
	fetched := s.get(strings.TrimPrefix(public, "https://atrium.test"))
	if fetched.Code != 200 || fetched.Body.Len() != 100 {
		t.Fatalf("signed link fetch: status %d, %d bytes", fetched.Code, fetched.Body.Len())
	}

	show := status(t, s.get("/media/library/tv/Lanterns%20%282026%29"), 200, "show")
	contains(t, show,
		"Media · Lanterns (2026)", "2 seasons.", `id="season-1"`, `id="season-2"`, "Season 00", "Season 01",
		"Episode 8", "Episode 0",
		`src="/media/art/season/Lanterns%20%282026%29/1"`, `src="/media/art/season/Lanterns%20%282026%29/0"`,
		`src="/media/thumb/tv/Lanterns%20%282026%29/Season%2001/`,
		`href="http://artemis.vpn:8085/Lanterns%20%282026%29/Season%2001/Lanterns%20%282026%29%20-%20S01E08.mkv"`)

	status(t, s.get("/media/library/tv/Nope"), 404, "unknown show")
	status(t, s.get("/media/library/movie/Nope"), 404, "unknown movie")
	status(t, s.get("/media/library/movie/%2e%2e"), 404, "dot-dot movie")
	status(t, s.get("/media/library/tv/%2e%2e"), 404, "dot-dot show")
	status(t, s.get("/media/library/movie/Dune%20%282021%29%2F..%2F.."), 404, "encoded slash")
}

func TestLibE2ELinksFollowSettingsAndEmptyLibrary(t *testing.T) {
	vars, root := libVars(t, map[string]string{"ATRIUM_CORE_PUBLIC_URL": "", "ATRIUM_MEDIA_MEDIA_MOVIES_URL": ""})
	libWrite(t, root, "movies/Dune (2021)/Dune (2021).mkv", 10)
	s := newSite(t, vars, Options{})
	movie := status(t, s.get("/media/library/movie/Dune%20%282021%29"), 200, "movie without links")
	lacks(t, movie, `title="Signed public link`, `title="Direct over the VPN"`)

	emptyVars, _ := libVars(t, nil)
	empty := newSite(t, emptyVars, Options{})
	page := status(t, empty.get("/media/library"), 200, "empty library")
	contains(t, page, "No movies yet", "Filed movies appear here.", "No shows yet", "Filed episodes appear here by show.", "Movies (0)", "TV (0)")

	bare := newSite(t, map[string]string{}, Options{})
	unset := status(t, bare.get("/media/library"), 200, "library without a root")
	contains(t, unset, "Library not configured", "ATRIUM_MEDIA_MEDIA_ROOT is not set")
	lacks(t, unset, "Movies (")
	status(t, bare.get("/media/library/movie/Dune"), 404, "movie without a root")
	status(t, bare.get("/media/library/tv/Lanterns"), 404, "show without a root")
}
