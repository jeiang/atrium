package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"testing"
)

type catalogOperationRow struct {
	Name     string `json:"name"`
	Bindings []struct {
		Kind   string `json:"kind"`
		Target string `json:"target"`
	} `json:"bindings"`
	APIRoutes []string `json:"api_routes"`
}

type catalogOperationsReport struct {
	Operations []catalogOperationRow `json:"operations"`
	Unbound    []struct {
		Module string `json:"module"`
		Kind   string `json:"kind"`
		Target string `json:"target"`
	} `json:"unbound"`
}

func runCatalogOperations(t *testing.T, args ...string) (catalogOperationsReport, int) {
	t.Helper()
	path := os.Getenv(builtBinaryEnv)
	if path == "" {
		t.Fatalf("%s must name the atrium binary the runner built", builtBinaryEnv)
	}
	cmd := exec.Command(path, append([]string{"catalog", "operations"}, args...)...)
	cmd.Env = []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("catalog operations %v: %v", args, err)
		}
		code = exit.ExitCode()
	}
	var report catalogOperationsReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("catalog operations %v printed non-JSON (exit %d, stderr %q): %v", args, code, stderr.String(), err)
	}
	return report, code
}

func hasOperation(rows []catalogOperationRow, name string) (catalogOperationRow, bool) {
	i := slices.IndexFunc(rows, func(r catalogOperationRow) bool { return r.Name == name })
	if i < 0 {
		return catalogOperationRow{}, false
	}
	return rows[i], true
}

func TestCatalogOperationsRunsWithoutDatabase(t *testing.T) {
	all, code := runCatalogOperations(t, "--json")
	if code != 0 {
		t.Fatalf("catalog operations --json exited %d", code)
	}
	identity, ok := hasOperation(all.Operations, "auth.identity")
	if !ok {
		t.Fatalf("auth.identity missing from %+v", all.Operations)
	}
	if !slices.ContainsFunc(identity.Bindings, func(b struct {
		Kind   string `json:"kind"`
		Target string `json:"target"`
	}) bool {
		return b.Kind == "api" && b.Target == "GET /me"
	}) {
		t.Fatalf("auth.identity bindings = %+v, want api GET /me", identity.Bindings)
	}

	missing, code := runCatalogOperations(t, "--json", "--missing-api")
	if code != 0 {
		t.Fatalf("catalog operations --missing-api exited %d", code)
	}
	if _, ok := hasOperation(missing.Operations, "auth.identity"); ok {
		t.Fatal("--missing-api lists auth.identity, which has an API binding")
	}
	for _, row := range missing.Operations {
		if len(row.APIRoutes) > 0 {
			t.Errorf("--missing-api lists %s with API routes %v", row.Name, row.APIRoutes)
		}
	}

	_, code = runCatalogOperations(t, "--json", "--require-api")
	want := 0
	if len(missing.Operations) > 0 {
		want = 1
	}
	if code != want {
		t.Fatalf("--require-api exited %d with %d operations missing an API binding, want %d", code, len(missing.Operations), want)
	}

	unbound, code := runCatalogOperations(t, "--json", "--unbound")
	if code != 0 {
		t.Fatalf("catalog operations --unbound exited %d", code)
	}
	for _, u := range unbound.Unbound {
		if u.Kind == "api" && u.Target == "GET /me" {
			t.Fatal("--unbound lists api GET /me, which auth.identity binds")
		}
	}
	if !slices.ContainsFunc(unbound.Unbound, func(u struct {
		Module string `json:"module"`
		Kind   string `json:"kind"`
		Target string `json:"target"`
	}) bool {
		return u.Kind == "cli" && u.Target == "catalog operations"
	}) {
		t.Fatalf("--unbound does not list the unbound catalog operations command: %+v", unbound.Unbound)
	}
}
