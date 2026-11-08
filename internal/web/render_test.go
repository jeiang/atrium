package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/web/tpl"
)

func TestFrameContributionsRunAndFailuresAreOmitted(t *testing.T) {
	rd, err := NewRenderer(config.Config{}, nil, tpl.Config{})
	if err != nil {
		t.Fatal(err)
	}
	rd.OnFrame(func(_ context.Context, f *tpl.Frame) error {
		f.PendingCount = 7
		return nil
	})
	rd.OnFrame(func(context.Context, *tpl.Frame) error { return errors.New("boom") })
	f, err := rd.Frame(httptest.NewRequest(http.MethodGet, "/", nil), "page", nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.PendingCount != 7 {
		t.Errorf("pending count %d, want 7", f.PendingCount)
	}
}
