package assistantmodule

import (
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/files"
)

const uploadBodyLimit = assistant.TuningAgentMessageCapBytes + 1<<20

func decodeUploads(reader *multipart.Reader, dst any) ([]files.UploadedFile, error) {
	var uploads []files.UploadedFile
	fields, total := 0, 0
	rv := reflect.ValueOf(dst).Elem()
	for count := 0; ; count++ {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return uploads, nil
		}
		if err != nil {
			return nil, err
		}
		if count >= 1000 {
			return nil, invalidField("upload", "Too many multipart parts.")
		}
		name := part.FormName()
		if part.FileName() != "" {
			if name != "upload" {
				return nil, invalidField(name, "Unknown file field.")
			}
			data, err := io.ReadAll(io.LimitReader(part, int64(assistant.TuningAgentFileCapBytes)+1))
			if err != nil {
				return nil, err
			}
			total += len(data)
			if len(data) > assistant.TuningAgentFileCapBytes || total > assistant.TuningAgentMessageCapBytes {
				return nil, &http.MaxBytesError{Limit: assistant.TuningAgentFileCapBytes}
			}
			if len(data) > 0 {
				uploads = append(uploads, files.UploadedFile{Name: part.FileName(), Type: part.Header.Get("Content-Type"), Data: data})
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, (1<<20)+1))
		if err != nil {
			return nil, err
		}
		fields += len(name) + len(data)
		if len(data) > 1<<20 || fields > 4<<20 {
			return nil, invalidField(name, "Multipart fields are too large.")
		}
		found := false
		for _, field := range reflect.VisibleFields(rv.Type()) {
			if field.Anonymous || strings.Split(field.Tag.Get("json"), ",")[0] != name || name == "-" {
				continue
			}
			found = true
			if err := uploadField(rv.FieldByIndex(field.Index), string(data)); err != nil {
				return nil, invalidField(name, err.Error())
			}
			break
		}
		if !found {
			return nil, invalidField(name, "Unknown multipart field.")
		}
	}
}

func multipartShape(area application.Area, status int, codes ...application.Code) application.APIShape {
	return application.APIShape{Area: area, Success: status, Body: application.BodyMultipart, BodyLimit: uploadBodyLimit, Errors: codes}
}

func uploadField(field reflect.Value, raw string) error {
	if field.Kind() == reflect.Pointer {
		value := reflect.New(field.Type().Elem())
		if err := uploadField(value.Elem(), raw); err != nil {
			return err
		}
		field.Set(value)
		return nil
	}
	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
	case reflect.Bool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		field.SetBool(v)
	case reflect.Float64:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("enter a finite number")
		}
		field.SetFloat(v)
	case reflect.Int64:
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		field.SetInt(v)
	default:
		return fmt.Errorf("unsupported multipart field")
	}
	return nil
}

func multipartJSONShape(area application.Area, status int, codes ...application.Code) application.APIShape {
	shape := multipartShape(area, status, codes...)
	shape.AllowJSON = true
	return shape
}
func (in *apiNewThread) DecodeMultipart(reader *multipart.Reader) error {
	uploads, err := decodeUploads(reader, in)
	in.Uploads = uploads
	return err
}
func (in *apiMessage) DecodeMultipart(reader *multipart.Reader) error {
	uploads, err := decodeUploads(reader, in)
	in.Uploads = uploads
	return err
}

func (in *apiEditRequest) DecodeMultipart(reader *multipart.Reader) error {
	uploads, err := decodeUploads(reader, in)
	in.Uploads = uploads
	return err
}
