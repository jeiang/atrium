package api

import (
	"context"
	"net/http"
	"reflect"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/commands"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
)

const actor = "operator"

func Query[I, O any](d *web.Deps, def application.Definition, shape application.APIShape, target string, run func(context.Context, I) (application.Read[O], error)) application.Binding {
	shape = defaults[I](shape, reflect.TypeFor[application.Read[O]](), application.BodyNone)
	if d != nil {
		mount(d, target, shape, func(w http.ResponseWriter, r *http.Request) {
			var in I
			if !decode(w, r, def.Name, &in, shape.Body, shape.IgnoreBody) {
				return
			}
			read, err := run(audit.With(r.Context(), actor, audit.API), in)
			if err != nil {
				web.APIFail(w, err)
				return
			}
			if shape.Uncached || shape.ReadOnlyPost {
				read.Revision = ""
			}
			if shape.Body == application.BodyBinary {
				body := any(read.Data).(Binary)
				if read.Revision != "" && notModified(w, r, read.Revision) {
					_ = body.File.Close()
					return
				}
				serveBinary(w, r, body)
				return
			}
			if read.Revision != "" && notModified(w, r, read.Revision) {
				return
			}
			write(w, shape, reflect.TypeFor[O](), read.Data, read)
		})
	}
	return binding(def, shape, target)
}

func Command[I, O any](d *web.Deps, def application.Definition, shape application.APIShape, target string, run func(context.Context, I) (application.Change[O], error)) application.Binding {
	body := application.BodyJSON
	if t := reflect.TypeFor[I](); t.Kind() == reflect.Struct && t.NumField() == 0 {
		body = application.BodyNone
	}
	shape = defaults[I](shape, reflect.TypeFor[application.Change[O]](), body)
	if d != nil {
		mount(d, target, shape, func(w http.ResponseWriter, r *http.Request) {
			var in I
			if !decode(w, r, def.Name, &in, shape.Body, shape.IgnoreBody) {
				return
			}
			if shape.Sensitive {
				commands.Withhold(r.Context())
			}
			change, err := run(audit.With(context.WithoutCancel(r.Context()), actor, audit.API), in)
			if err != nil {
				web.APIFail(w, err)
				return
			}
			write(w, shape, reflect.TypeFor[O](), change.Data, change)
		})
	}
	return binding(def, shape, target)
}

func defaults[I any](shape application.APIShape, response reflect.Type, body application.BodyKind) application.APIShape {
	if shape.Request == nil {
		shape.Request = reflect.TypeFor[I]()
	}
	if shape.Response == nil {
		shape.Response = response
	}
	if shape.Success == 0 {
		shape.Success = http.StatusOK
	}
	if shape.Body == "" {
		shape.Body = body
	}
	return shape
}

func binding(def application.Definition, shape application.APIShape, target string) application.Binding {
	return application.Binding{Kind: application.API, Target: target, Uses: []application.Name{def.Name}, API: &shape}
}

func mount(d *web.Deps, target string, shape application.APIShape, h http.HandlerFunc) {
	method, pattern, _ := application.Route(target)
	d.DeclaredAPI(method, pattern, shape, h)
}

func write(w http.ResponseWriter, shape application.APIShape, data reflect.Type, raw, envelope any) {
	switch {
	case shape.Success == http.StatusNoContent:
		w.WriteHeader(http.StatusNoContent)
	case shape.Response == data:
		web.JSON(w, shape.Success, raw)
	default:
		web.JSON(w, shape.Success, envelope)
	}
}
