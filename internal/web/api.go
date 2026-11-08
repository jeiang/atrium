package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	APIPrefix    = "/api/v1"
	SourceBearer = "bearer"

	apiPrefix = "/api/"
	jsonType  = "application/json"
	realm     = `Bearer realm="atrium"`
)

type apiError struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  []FieldError `json:"fields,omitempty"`
}

func isAPI(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, apiPrefix) || r.URL.Path == strings.TrimSuffix(apiPrefix, "/")
}

func withIdentity(ctx context.Context, who Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, who)
}

func me(w http.ResponseWriter, r *http.Request) {
	who, _ := IdentityOf(r.Context())
	JSON(w, http.StatusOK, map[string]any{
		"subject": who.Subject, "email": who.Email, "name": who.Name, "admin": who.Admin, "api_version": 1,
	})
}

const (
	pageDefault = 50
	pageMax     = 100
)

func EncodeCursor(v any) *string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	s := base64.RawURLEncoding.EncodeToString(raw)
	return &s
}

func Paging(w http.ResponseWriter, r *http.Request, cursor any) (limit int, after bool, ok bool) {
	query := r.URL.Query()
	limit = pageDefault
	if raw := QueryValue(query, "limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > pageMax {
			APIError(w, http.StatusUnprocessableEntity, "invalid", "limit must be between 1 and 100.",
				FieldError{Type: "int_range", Loc: []string{"query", "limit"}, Msg: "Input should be between 1 and 100", Input: raw})
			return 0, false, false
		}
		limit = n
	}
	raw := QueryValue(query, "cursor")
	if raw == "" {
		return limit, false, true
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		err = dec.Decode(cursor)
	}
	if err != nil {
		APIError(w, http.StatusBadRequest, "bad_cursor", "The cursor is not valid.")
		return 0, false, false
	}
	return limit, true, true
}

func PathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(PathParam(r, name), 10, 64)
	if err != nil || id < 1 {
		APIError(w, http.StatusNotFound, "not_found", "Not found.")
		return 0, false
	}
	return id, true
}

type List[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	w.Header().Set("Content-Type", jsonType)
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func APIError(w http.ResponseWriter, status int, code, message string, fields ...FieldError) {
	JSON(w, status, map[string]apiError{"error": {Code: code, Message: message, Fields: fields}})
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil && dec.More() {
		err = errors.New("request body must hold one JSON object")
	}
	var tooLarge *http.MaxBytesError
	var mistyped *json.UnmarshalTypeError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooLarge):
		APIError(w, http.StatusRequestEntityTooLarge, "too_large", "Request too large.")
	case errors.As(err, &mistyped):
		APIError(w, http.StatusUnprocessableEntity, "invalid", "A field has the wrong type.",
			FieldError{Type: "type_error", Loc: []string{"body", mistyped.Field}, Msg: "Input should be " + mistyped.Type.String()})
	case errors.Is(err, io.EOF):
		APIError(w, http.StatusBadRequest, "bad_request", "The request body is empty.")
	default:
		APIError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
	return false
}

func (d *Deps) API(method, pattern string, h http.HandlerFunc) {
	d.route(method, APIPrefix+pattern, accessDefault, h)
}

func (d *Deps) APIAdmin(method, pattern string, h http.HandlerFunc) {
	d.route(method, APIPrefix+pattern, accessAdmin, h)
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	return token, found && strings.EqualFold(scheme, "Bearer") && token != ""
}

func (g *Guard) api(w http.ResponseWriter, r *http.Request, next http.Handler) {
	h := w.Header()
	setSecurityHeaders(h)
	h.Set("Cache-Control", "no-store")
	if g.sessions == nil || !g.auth.OIDC() {
		APIError(w, http.StatusNotFound, "api_unavailable", "The API needs OIDC sign-in.")
		return
	}
	token, ok := bearerToken(r)
	if !ok {
		h.Set("WWW-Authenticate", realm)
		APIError(w, http.StatusUnauthorized, "unauthenticated", "A bearer access token is required.")
		return
	}
	s, err := g.sessions.AuthenticateBearer(r.Context(), token)
	switch {
	case err == nil:
	case errors.Is(err, oidc.ErrAPIOff):
		APIError(w, http.StatusNotFound, "api_unavailable", "The API is not configured.")
		return
	case errors.Is(err, oidc.ErrToken):
		h.Set("WWW-Authenticate", realm+`, error="invalid_token"`)
		APIError(w, http.StatusUnauthorized, "invalid_token", "The access token was refused.")
		return
	case errors.Is(err, oidc.ErrDenied):
		APIError(w, http.StatusForbidden, "forbidden", "This account has no access.")
		return
	default:
		log.Error("bearer authentication failed", logx.Err, err.Error())
		APIError(w, http.StatusServiceUnavailable, "auth_unavailable", "Sign-in is unavailable.")
		return
	}
	who := Identity{Source: SourceBearer, Subject: s.Subject, Email: s.Email, Name: s.Name, Admin: s.Admin}
	if !who.Admin && g.classify != nil && g.classify(r) == accessAdmin {
		APIError(w, http.StatusForbidden, "admin_required", "Administrator access is required.")
		return
	}
	if isUnsafe(r.Method) {
		if !sameOrigin(r) {
			APIError(w, http.StatusForbidden, "cross_origin", "Cross-origin requests are refused.")
			return
		}
		if r.ContentLength != 0 {
			mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mediaType != jsonType {
				APIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Send application/json.")
				return
			}
		}
		limit := defaultBodyCap
		if g.bodyCap != nil {
			if limit, err = g.bodyCap(r.Context()); err != nil {
				log.Error("body cap setting failed", logx.Err, err.Error())
				APIError(w, http.StatusInternalServerError, "internal", "Internal error.")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, int64(limit))
	}
	ctx := withIdempotency(withIdentity(r.Context(), who), g.idem)
	next.ServeHTTP(w, r.WithContext(ctx))
}
