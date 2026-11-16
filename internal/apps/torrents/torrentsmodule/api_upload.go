package torrentsmodule

import (
	"encoding/base64"
	"io"
	"mime/multipart"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/torrents"
	torrentsapp "github.com/jeiang/atrium/internal/apps/torrents/application"
)

func addShape() application.APIShape {
	shape := shape[MediaAdded]("media")
	shape.Body = application.BodyMultipart
	shape.AllowJSON = true
	shape.BodyLimit = 2 << 20
	shape.MultipartFile = "torrent"
	shape.MultipartAlternative = "magnet"
	return shape
}

func (in *MediaAddInput) DecodeMultipart(reader *multipart.Reader) error {
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := part.FormName()
		if seen[name] || (name != "magnet" && name != "torrent") {
			return &application.Error{Code: application.Invalid, Message: "Give one magnet or torrent file."}
		}
		seen[name] = true
		limit := torrents.TuningMediaTorrentUploadBytes / 4 * 3
		if name == "magnet" {
			limit = 1 << 20
		}
		data, err := io.ReadAll(io.LimitReader(part, int64(limit+1)))
		if err != nil {
			return err
		}
		if len(data) > limit {
			return &application.Error{Code: application.Invalid, Message: "The torrent input is too large."}
		}
		if name == "magnet" {
			if part.FileName() != "" {
				return &application.Error{Code: application.Invalid, Message: "magnet must be a text field."}
			}
			in.Magnet = string(data)
		} else {
			if part.FileName() == "" {
				return &application.Error{Code: application.Invalid, Message: "torrent must be a file."}
			}
			in.Torrent = &torrentsapp.Torrent{Name: part.FileName(), Encoded: base64.StdEncoding.EncodeToString(data)}
		}
	}
}
