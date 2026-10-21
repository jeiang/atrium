package pages

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
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
	contains(t, unset, "Library not configured", "media_root is not set; pin it with ATRIUM_MEDIA_MEDIA_ROOT")
	lacks(t, unset, "Movies (")
	status(t, bare.get("/media/library/movie/Dune"), 404, "movie without a root")
	status(t, bare.get("/media/library/tv/Lanterns"), 404, "show without a root")
}

func TestLibE2ELinkTTLFollowsTheRegistry(t *testing.T) {
	vars, root := libVars(t, nil)
	libTree(t, root)
	s := newSite(t, vars, Options{})
	ctx := audit.With(context.Background(), "test", audit.CLI)

	err := settings.Default.Set(ctx, s.db.Write, media.KeyLinkTTL, "59", 0)
	if err == nil || !strings.Contains(err.Error(), "media_link_ttl_seconds must be between 60 and 2592000") {
		t.Fatalf("ttl 59: %v", err)
	}
	if err := settings.Default.Set(ctx, s.db.Write, media.KeyLinkTTL, "7200", 0); err != nil {
		t.Fatal(err)
	}
	movie := status(t, s.get("/media/library/movie/Dune%20%282021%29"), 200, "movie")
	contains(t, movie, "expire after 2 hours")
	show := status(t, s.get("/media/library/tv/Lanterns%20%282026%29"), 200, "show")
	contains(t, show, "expire after 2 hours")
	link, err := url.Parse(libHref(t, movie, "https://atrium.test/media/file/"))
	if err != nil {
		t.Fatal(err)
	}
	exp, err := strconv.ParseInt(link.Query().Get("exp"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if ahead := exp - time.Now().Unix(); ahead < 7100 || ahead > 7205 {
		t.Errorf("link expires in %d s, want about 7200", ahead)
	}
}

func TestLibE2ELinkTTLReadsAsAHumanDuration(t *testing.T) {
	vars, root := libVars(t, nil)
	libTree(t, root)
	s := newSite(t, vars, Options{})
	ctx := audit.With(context.Background(), "test", audit.CLI)
	for ttl, want := range map[string]string{
		"1800": "expire after 30 minutes", "3540": "expire after 59 minutes", "5400": "expire after 90 minutes",
		"60": "expire after 1 minute;", "3600": "expire after 1 hour;", "172800": "expire after 2 days",
	} {
		if err := settings.Default.Set(ctx, s.db.Write, media.KeyLinkTTL, ttl, 0); err != nil {
			t.Fatal(err)
		}
		contains(t, status(t, s.get("/media/library/movie/Dune%20%282021%29"), 200, "movie "+ttl), want)
	}
}
