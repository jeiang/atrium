package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/auth/oidc/oidctest"
	"github.com/jeiang/atrium/internal/config"
)

const browsersEnv = "ATRIUM_E2E_BROWSERS"

func TestGuardBrowserSignInReturnsAcrossSites(t *testing.T) {
	engines := os.Getenv(browsersEnv)
	if engines == "" {
		t.Skip(browsersEnv + " names the Playwright projects to drive, for example chromium,webkit")
	}
	web, err := filepath.Abs(filepath.Join("..", "..", "web"))
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range strings.Split(engines, ",") {
		engine = strings.TrimSpace(engine)
		t.Run(engine, func(t *testing.T) { driveSignIn(t, web, engine) })
	}
}

func driveSignIn(t *testing.T, web, engine string) {
	fd, port := handOver(t)
	base := "http://localhost:" + port
	idp := oidctest.New(t, oidctest.Options{ClientSecret: "s3cret-for-the-test"})
	idp.AddUser(oidctest.User{Name: "admin", Email: "boss@example.com", Groups: []string{"atrium_access", "atrium_admin"}})
	idp.Approve("admin")
	dir := t.TempDir()
	setEnv(t, settingEnv(config.KeyPublicURL), base)
	setEnv(t, "ATRIUM_HOST_CONFIG", oidcHostConfig(t, dir, oidcHost{
		Issuer:      idp.Issuer(),
		ClientID:    idp.ClientID(),
		Secret:      "s3cret-for-the-test",
		GroupsClaim: "groups",
	}))
	wimServeIn(t, dir, "--listen-fd", fd, "--auth", "oidc", "--allowed-host", "localhost")
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><html lang="en"><title>Mail</title><a href="%s/third-party-notices.txt?from=mail">Open in Atrium</a></html>`, base)
	}))
	t.Cleanup(elsewhere.Close)
	cmd := exec.Command(filepath.Join(web, "node_modules", ".bin", "playwright"), "test", "--config", "playwright.live.config.ts", "--project", engine)
	cmd.Dir = web
	cmd.Env = append(os.Environ(), "ATRIUM_LIVE_URL="+base, "ATRIUM_LIVE_ELSEWHERE="+elsewhere.URL)
	out, err := cmd.CombinedOutput()
	t.Logf("playwright %s:\n%s", engine, out)
	if err != nil {
		t.Fatalf("playwright %s: %v", engine, err)
	}
}
