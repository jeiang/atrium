package application

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
)

const AddName application.Name = "media.add"

type Magnet struct {
	URI string
}

type Torrent struct {
	Name    string
	Encoded string
}

type AddInput struct {
	Magnet     *Magnet
	Torrent    *Torrent
	Media      bool
	ProposalID *int64
}

type Added struct {
	Hash  string
	Kind  string
	Shown string
}

func DescribeAdd() application.Definition {
	return application.Describe[AddInput, Added](AddName, application.Command,
		[]application.Authority{application.Operator, application.Approved}, application.External, true)
}

func CheckMagnet(uri string) (string, error) {
	if strings.TrimSpace(uri) == "" {
		return "", invalid("magnet", "Paste a magnet link or choose a .torrent file.")
	}
	if !strings.HasPrefix(uri, "magnet:?") {
		return "", invalid("magnet", "That is not a magnet link.")
	}
	hash, ok := media.MagnetHash(uri)
	if !ok {
		return "", invalid("magnet", "The magnet link has no v1 info hash (xt=urn:btih:...).")
	}
	return hash, nil
}

func (s *Service) CheckMagnet(ctx context.Context, uri string) error {
	_, err := CheckMagnet(uri)
	if err != nil {
		_, err = s.record(ctx, AddName, "", nil, err)
	}
	return err
}

func (s *Service) Add(ctx context.Context, in AddInput) (application.Change[Added], error) {
	out, err := s.add(ctx, in)
	id, err := s.record(ctx, AddName, out.Hash, map[string]any{"kind": kindOf(in.Media), "proposal_id": in.ProposalID}, err)
	if err != nil {
		return application.Change[Added]{}, err
	}
	return application.Change[Added]{Data: out, AuditID: id}, nil
}

func (s *Service) add(ctx context.Context, in AddInput) (Added, error) {
	out := Added{Kind: kindOf(in.Media), Shown: "the magnet link"}
	if in.Magnet != nil && in.Torrent != nil {
		return out, &application.Error{Code: application.Invalid, Message: "Give a magnet link or a .torrent file, not both.", Fields: []application.Field{
			{Name: "magnet", Code: "invalid", Message: "Give a magnet link or a .torrent file, not both."},
			{Name: "torrent", Code: "invalid", Message: "Give a magnet link or a .torrent file, not both."},
		}}
	}
	set, err := s.settings(ctx)
	if err != nil {
		return out, err
	}
	req := media.AddRequest{}
	if in.Media {
		req.Tags = media.TagMedia
	}
	if in.Torrent != nil {
		data, err := decodeTorrent(in.Torrent.Encoded, set.TorrentUpload)
		if err != nil {
			return out, err
		}
		if out.Hash, err = media.TorrentHash(data); err != nil {
			return out, invalid("torrent", err.Error())
		}
		req.Data, req.Name = data, strings.TrimSpace(in.Torrent.Name)
		if req.Name == "" {
			req.Name = "upload.torrent"
		}
		out.Shown = strings.TrimSpace(in.Torrent.Name)
		if out.Shown == "" {
			out.Shown = "the torrent"
		}
	} else {
		uri := ""
		if in.Magnet != nil {
			uri = strings.TrimSpace(in.Magnet.URI)
		}
		if out.Hash, err = CheckMagnet(uri); err != nil {
			return out, err
		}
		req.Magnet = uri
	}
	if in.ProposalID != nil {
		if inv, ok := application.InvocationOf(ctx); ok {
			req.Scope = media.Scope{ProposalID: in.ProposalID, ThreadID: &inv.ThreadID, RunID: &inv.RunID}
		} else {
			req.Scope = media.Scope{ProposalID: in.ProposalID}
		}
	}
	if _, err := set.Add(ctx, s.d.Ledger, set, req); err != nil {
		return out, classifyWrite(err)
	}
	if in.Media {
		if _, err := set.SelectFiles(ctx, s.d.Ledger, set, req.Scope, nil); err != nil {
			return out, &application.Error{Code: application.Partial, Message: "Torrent added, but file selection failed.", Cause: err}
		}
	}
	return out, nil
}

func decodeTorrent(b64 string, limit int) ([]byte, error) {
	b64 = strings.Join(strings.Fields(b64), "")
	if b64 == "" {
		return nil, invalid("torrent", "Paste a magnet link or choose a .torrent file.")
	}
	n := len(b64)
	if n > limit {
		return nil, invalid("torrent", "the torrent file is too large")
	}
	b64 = strings.NewReplacer("-", "+", "_", "/").Replace(b64)
	b64 += strings.Repeat("=", (4-n%4)%4)
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, invalid("torrent", "the torrent data is not base64")
	}
	return data, nil
}

func kindOf(isMedia bool) string {
	if isMedia {
		return "media"
	}
	return "plain"
}
