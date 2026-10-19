package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestCodemodeWorkerIsRemoved(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := newApp().Run(context.Background(), []string{"codemode-worker"}, &stdout, &stderr)
	if code == cli.ExitOK {
		t.Fatal("codemode-worker exited 0")
	}
	if !strings.Contains(stderr.String(), "removed in v2") {
		t.Errorf("stderr = %q, want the removed in v2 text", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}
