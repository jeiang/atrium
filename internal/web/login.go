package web

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
)

var loginTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Sign in · Atrium</title>
<style>
body{margin:0;min-height:100vh;display:grid;place-items:center;font:16px/1.5 system-ui,sans-serif;background:Canvas;color:CanvasText}
main{width:min(22rem,calc(100vw - 2rem));display:flex;flex-direction:column;gap:1rem}
h1{margin:0;font-size:1.25rem;font-weight:600}
p[role]{margin:0;padding:.75rem 1rem;border:1px solid color-mix(in srgb,CanvasText 20%,transparent);border-radius:.5rem}
p[data-tone=destructive]{border-color:#c62828;color:#c62828}
button{width:100%;padding:.6rem 1rem;font:inherit;font-weight:600;border:0;border-radius:.5rem;background:CanvasText;color:Canvas;cursor:pointer}
</style>
</head>
<body>
<main>
<h1>Sign in</h1>
{{if .Message}}<p data-tone="{{.Tone}}" role="{{if eq .Tone "destructive"}}alert{{else}}status{{end}}">{{.Message}}</p>{{end}}
{{if .Start}}<form method="post" action="/login">
<input type="hidden" name="csrf_token" value="{{.CSRF}}">
<input type="hidden" name="next" value="{{.Next}}">
<button type="submit" autofocus>{{.Button}}</button>
</form>{{end}}
</main>
</body>
</html>
`))

type loginDocument struct {
	loginView
	CSRF string
}

func writeLogin(w http.ResponseWriter, r *http.Request, status int, view loginView) error {
	var body bytes.Buffer
	if err := loginTemplate.Execute(&body, loginDocument{loginView: view, CSRF: CSRFToken(r.Context())}); err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(body.Len()))
	w.WriteHeader(status)
	_, err := w.Write(body.Bytes())
	return err
}
