package main

import (
	"context"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
)

func TestRefreshCommandsExitZeroWhileMailIsOff(t *testing.T) {
	f := newFixture(t, loadGoldenData(t), map[string]string{flagEnv(module.Mail): "false"}, "")
	for _, argv := range [][]string{{"loops", "refresh"}, {"watches", "refresh"}} {
		code, stdout, stderr := f.Run(context.Background(), argv...)
		if code != cli.ExitOK || stdout != "" {
			t.Errorf("%v with mail off: exit %d stdout %q stderr %q", argv, code, stdout, stderr)
		}
	}
}
