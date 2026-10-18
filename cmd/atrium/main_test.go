package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
)

func TestNoSubcommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := newApp().Run(context.Background(), nil, &stdout, &stderr); code != cli.ExitUsage {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := newApp().Run(context.Background(), []string{"--help"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}
	if !bytes.HasPrefix(stdout.Bytes(), []byte("usage: atrium [-h]")) {
		t.Errorf("stdout = %q, want usage line", stdout.String())
	}
}
