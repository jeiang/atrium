package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/auth/bearer"
	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	APIPrefix       = "/api/v1"
	SourceBearer    = "bearer"
	RequestIDHeader = "Atrium-Request-Id"
	ProblemPrefix   = "urn:atrium:problem:"

	apiPrefix = "/api/"
	jsonType  = "application/json"
	realm     = `Bearer realm="atrium"`
)

type apiError struct {
	Code         string       `json:"code"`
	Message      string       `json:"message"`
	Fields       []FieldError `json:"fields,omitempty"`
	EffectIDs    []int64      `json:"effect_ids,omitempty"`
	RetryAfter   int64        `json:"retry_after,omitempty"`
	DiagnosticID string       `json:"diagnostic_id,omitempty"`
	Type         string       `json:"type,omitempty"`
	Status       int          `json:"status,omitempty"`
	Instance     string       `json:"instance,omitempty"`
	CommandID    string       `json:"command_id,omitempty"`
}

func newAPIError(w http.ResponseWriter, status int, code, message string) apiError {
	return apiError{Code: code, Message: message, Type: ProblemPrefix + code, Status: status, Instance: w.Header().Get(RequestIDHeader)}
}

func isAPI(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, apiPrefix) || r.URL.Path == strings.TrimSuffix(apiPrefix, "/")
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
	body := newAPIError(w, status, code, message)
	body.Fields = fields
	JSON(w, status, map[string]apiError{"error": body})
}

func CommandError(w http.ResponseWriter, status int, code, message, commandID string) {
	body := newAPIError(w, status, code, message)
	body.CommandID = commandID
	JSON(w, status, map[string]apiError{"error": body})
}

func APIErrorWith(w http.ResponseWriter, status int, code, message string, extra map[string]any) {
	body := map[string]any{"error": newAPIError(w, status, code, message)}
	maps.Copy(body, extra)
	JSON(w, status, body)
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSON(w, r, v, false)
}

func DecodeOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSON(w, r, v, true)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any, optional bool) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if optional && errors.Is(err, io.EOF) {
		return true
	}
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
	if d.commands != nil && isUnsafe(method) {
		h = d.commands(APIPrefix+pattern, h).ServeHTTP
	}
	d.route(method, APIPrefix+pattern, accessDefault, h)
}

func (d *Deps) DeclaredAPI(method, pattern string, shape application.APIShape, h http.HandlerFunc) {
	d.API(method, pattern, h)
	(*d.routes)[len(*d.routes)-1].api = &shape
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
	h.Set(RequestIDHeader, rand.Text())
	token, ok := bearerToken(r)
	addr, addrOK := g.clientAddr(r)
	if !addrOK {
		APIError(w, http.StatusBadRequest, "bad_request", "The forwarded address is not valid.")
		return
	}
	charges := []limitCharge{{class: classAPIAddress, key: addr.String()}}
	if ok && boundedToken(token) {
		charges = append(charges, limitCharge{class: classAPIToken, key: tokenKey(token)})
	}
	if wait, allowed := g.limits.charge(g.limits.now(), charges...); !allowed {
		retryAfter(w, wait)
		APIError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests.")
		return
	}
	if !ok {
		h.Set("WWW-Authenticate", realm)
		APIError(w, http.StatusUnauthorized, "unauthenticated", "A bearer access token is required.")
		return
	}
	if g.bearers == nil {
		APIError(w, http.StatusServiceUnavailable, "auth_unavailable", "Sign-in is unavailable.")
		return
	}
	tok, err := g.bearers.Authenticate(r.Context(), token)
	switch {
	case err == nil:
	case errors.Is(err, oidc.ErrAPIOff):
		APIError(w, http.StatusNotFound, "api_unavailable", "The API is not configured.")
		return
	case errors.Is(err, bearer.ErrInvalid), errors.Is(err, bearer.ErrRevoked), errors.Is(err, bearer.ErrExpired), errors.Is(err, oidc.ErrToken):
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
	who := Identity{Source: SourceBearer, Subject: tok.Subject, Email: tok.Email, Name: tok.DisplayName}
	if isUnsafe(r.Method) {
		if !sameOrigin(r) {
			APIError(w, http.StatusForbidden, "cross_origin", "Cross-origin requests are refused.")
			return
		}
		var shape *application.APIShape
		if g.apiShape != nil {
			shape = g.apiShape(r)
		}
		if shape != nil && shape.BeforeBody != nil {
			if err := shape.BeforeBody(r.Context()); err != nil {
				APIFail(w, err)
				return
			}
		}
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		multipart := mediaType == "multipart/form-data" && shape != nil && shape.Body == application.BodyMultipart
		if r.ContentLength != 0 && (shape == nil || !shape.IgnoreBody) {
			if (mediaType != jsonType && !multipart) || (mediaType == jsonType && shape != nil && shape.Body == application.BodyMultipart && !shape.AllowJSON) {
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
		cap := int64(limit)
		if multipart {
			cap = shape.BodyLimit
		}
		if r.ContentLength > cap && (shape == nil || !shape.IgnoreBody) {
			APIError(w, http.StatusRequestEntityTooLarge, "too_large", "Request too large.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, cap)
	}
	ctx := withStreamAccess(withAPIIdentity(r.Context(), who), "bearer:"+tokenKey(token), func(ctx context.Context) error {
		_, err := g.bearers.Authenticate(ctx, token)
		return err
	})
	next.ServeHTTP(w, r.WithContext(ctx))
}
