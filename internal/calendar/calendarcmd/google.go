package calendarcmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/cli"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
)

func googleAuthCommand(open connectionsapp.Open) *cli.Command {
	return &cli.Command{Name: "google-auth", Setup: func(a *cli.Args) cli.Handler {
		port := a.Int("port", 0, cli.Opt{})
		return func(e *cli.Env) int {
			svc, closeDB, err := open(e.Ctx)
			if err != nil {
				return cli.Fail(e, err)
			}
			defer closeDB()
			if err := runGoogleAuth(e, svc, *port); err != nil {
				return cli.Fail(e, err)
			}
			_, _ = fmt.Fprintln(e.Stdout, "\nGoogle Calendar consent stored in google/default.")
			return cli.ExitOK
		}
	}}
}

func runGoogleAuth(e *cli.Env, svc *connectionsapp.Service, port int) error {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	result := make(chan map[string]string, 1)
	served := make(chan struct{}, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, fmt.Sprintf("Unsupported method ('%s')", r.Method), http.StatusNotImplemented)
			return
		}
		values := r.URL.Query()
		params := map[string]string{"state": values.Get("state"), "code": values.Get("code"), "error": values.Get("error")}
		if params["code"] != "" || params["error"] != "" {
			select {
			case result <- params:
			default:
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><title>atrium</title><p>Authorised. You can close this tab.")
		select {
		case served <- struct{}{}:
		default:
		}
	})}
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(e.Ctx), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	redirect := fmt.Sprintf("http://127.0.0.1:%d", listener.Addr().(*net.TCPAddr).Port)
	started, err := svc.OAuthStart(e.Ctx, connectionsapp.OAuthStartInput{Capability: "calendar", Redirect: redirect, Loopback: true})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.Stdout, "Open this URL in a browser and approve access:\n\n%s\n\n", started.Data.URL)
	got, err := waitForRedirect(e.Ctx, result, served, 300)
	if err != nil {
		return err
	}
	if got == nil {
		return &application.Error{Code: application.Refused, Message: "timed out waiting for the Google redirect"}
	}
	_, err = svc.OAuthComplete(e.Ctx, connectionsapp.OAuthCompleteInput{State: got["state"], Code: got["code"], AuthError: got["error"], Loopback: true})
	return err
}

func waitForRedirect(ctx context.Context, result chan map[string]string, served chan struct{}, limit int) (map[string]string, error) {
	for waited := 0; waited < limit; waited++ {
		select {
		case got := <-result:
			return got, nil
		default:
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-served:
			timer.Stop()
		case <-timer.C:
		}
	}
	select {
	case got := <-result:
		return got, nil
	default:
		return nil, nil
	}
}
