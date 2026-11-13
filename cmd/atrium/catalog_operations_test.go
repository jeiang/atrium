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
	Name      string                    `json:"name"`
	Area      string                    `json:"area"`
	Authority []string                  `json:"authority"`
	Bindings  []catalogOperationBinding `json:"bindings"`
	APIRoutes []string                  `json:"api_routes"`
}

type catalogOperationBinding struct {
	Kind      string   `json:"kind"`
	Target    string   `json:"target"`
	Delegates []string `json:"delegates"`
}

type catalogOperationsReport struct {
	Operations []catalogOperationRow `json:"operations"`
	Unbound    []struct {
		Module string `json:"module"`
		Kind   string `json:"kind"`
		Target string `json:"target"`
	} `json:"unbound"`
	ProtocolOnly []struct {
		Module   string `json:"module"`
		Listener string `json:"listener"`
		Target   string `json:"target"`
		Reason   string `json:"reason"`
	} `json:"protocol_only"`
}

func runCatalogOperations(t *testing.T, args ...string) (catalogOperationsReport, int) {
	t.Helper()
	path := os.Getenv(builtBinaryEnv)
	if path == "" {
		t.Skipf("catalog operations runs the built binary; %s is set by the e2e runner", builtBinaryEnv)
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

func TestCatalogOperationsReportsAreasAndMissingAPI(t *testing.T) {
	all, code := runCatalogOperations(t, "--json")
	if code != 0 {
		t.Fatalf("catalog operations --json exited %d", code)
	}
	identity, ok := hasOperation(all.Operations, "auth.identity")
	if !ok {
		t.Fatalf("auth.identity missing from %+v", all.Operations)
	}
	if !slices.ContainsFunc(identity.Bindings, func(b catalogOperationBinding) bool {
		return b.Kind == "api" && b.Target == "GET /me"
	}) {
		t.Fatalf("auth.identity bindings = %+v, want api GET /me", identity.Bindings)
	}
	if identity.Area != "settings" {
		t.Fatalf("auth.identity area = %q, want settings", identity.Area)
	}
	setting, ok := hasOperation(all.Operations, "settings.set")
	if !ok {
		t.Fatal("settings.set missing")
	}
	for _, target := range []string{"POST /settings/assistant", "POST /settings/permissions"} {
		if !slices.ContainsFunc(setting.Bindings, func(b catalogOperationBinding) bool {
			return b.Kind == "html" && b.Target == target && slices.Contains(b.Delegates, "settings.set")
		}) {
			t.Fatalf("missing explicit settings delegation for %s", target)
		}
	}
	for _, name := range []string{"connections.oauth-start", "connections.oauth-complete"} {
		op, ok := hasOperation(all.Operations, name)
		if !ok || !slices.ContainsFunc(op.Bindings, func(b catalogOperationBinding) bool {
			return b.Kind == "cli" && b.Target == "calendar google-auth" && slices.Contains(b.Delegates, name)
		}) {
			t.Fatalf("missing explicit calendar OAuth delegation for %s", name)
		}
	}
	recovery, metrics := false, false
	for _, binding := range all.ProtocolOnly {
		if binding.Reason == "" {
			t.Fatalf("protocol-only binding without a reason: %+v", binding)
		}
		recovery = recovery || (binding.Module == "core" && binding.Target == "recovery" && binding.Reason == "lifecycle")
		metrics = metrics || (binding.Listener == "ops" && binding.Target == "GET /metrics" && binding.Reason == "transport")
	}
	if !recovery || !metrics {
		t.Fatalf("protocol lifecycle/ops inventory incomplete: %+v", all.ProtocolOnly)
	}

	missing, code := runCatalogOperations(t, "--json", "--missing-api")
	if code != 0 {
		t.Fatalf("catalog operations --missing-api exited %d", code)
	}
	if _, ok := hasOperation(missing.Operations, "auth.identity"); ok {
		t.Fatal("--missing-api lists auth.identity, which has an API binding")
	}
	for _, row := range missing.Operations {
		if row.Area != "" {
			t.Errorf("--missing-api lists %s, which has a typed API binding in area %s", row.Name, row.Area)
		}
		if !slices.ContainsFunc(row.Authority, func(a string) bool {
			return a == "operator" || a == "operator_send" || a == "approved"
		}) {
			t.Errorf("--missing-api lists %s with authorities %v, none of them operator", row.Name, row.Authority)
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
	if len(unbound.Unbound) != 0 {
		t.Fatalf("unbound adapters: %+v", unbound.Unbound)
	}
	if len(missing.Operations) == 0 {
		t.Fatal("missing API report unexpectedly empty before the mail API cutover")
	}
}
