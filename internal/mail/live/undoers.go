package live

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

const (
	OpMoveBack     = "move_back"
	OpGmailLabels  = "gmail_labels"
	OpGmailUntrash = "gmail_untrash"
	OpUnflag       = "unflag"
)

type MoveBackItem struct {
	ID      int64   `json:"id"`
	MsgID   string  `json:"msgid"`
	UID     uint32  `json:"uid"`
	DestUID *uint32 `json:"dest_uid"`
}

type MoveBack struct {
	Account         string         `json:"account"`
	SrcIMAP         string         `json:"src_imap"`
	DestIMAP        string         `json:"dest_imap"`
	DestUIDValidity *uint32        `json:"dest_uidvalidity"`
	Items           []MoveBackItem `json:"items"`
}

type GmailLabels struct {
	Account     string   `json:"account"`
	IMAPName    string   `json:"imap_name"`
	UIDValidity uint32   `json:"uidvalidity"`
	UIDs        []uint32 `json:"uids"`
	Add         []string `json:"add"`
	Remove      []string `json:"remove"`
}

type GmailUntrash struct {
	Account         string   `json:"account"`
	TrashIMAP       string   `json:"trash_imap"`
	DestUIDValidity *uint32  `json:"dest_uidvalidity"`
	UIDs            []uint32 `json:"uids"`
}

type Unflag struct {
	Account     string   `json:"account"`
	IMAPName    string   `json:"imap_name"`
	UIDValidity uint32   `json:"uidvalidity"`
	UIDs        []uint32 `json:"uids"`
}

func RegisterUndoers(r *proposals.Registry, open mailops.Opener) {
	r.Undoers[OpMoveBack] = plan(func(ctx context.Context, p MoveBack) (py.Dict, error) { return undoMoveBack(ctx, open, p) })
	r.Undoers[OpGmailLabels] = plan(func(ctx context.Context, p GmailLabels) (py.Dict, error) { return undoGmailLabels(ctx, open, p) })
	r.Undoers[OpGmailUntrash] = plan(func(ctx context.Context, p GmailUntrash) (py.Dict, error) { return undoGmailUntrash(ctx, open, p) })
	r.Undoers[OpUnflag] = plan(func(ctx context.Context, p Unflag) (py.Dict, error) { return undoUnflag(ctx, open, p) })
}

func plan[T any](fn func(context.Context, T) (py.Dict, error)) proposals.Undoer {
	return func(ctx context.Context, raw json.RawMessage) (py.Dict, error) {
		var p T
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return fn(ctx, p)
	}
}

func gmailAccount(recorded string) string {
	if recorded == "" {
		return config.Gmail
	}
	return recorded
}

func validity(ctx context.Context, client mail.Client, imapName string) (uint32, error) {
	st, err := client.Status(ctx, imapName)
	return st.UIDValidity, err
}

func undoMoveBack(ctx context.Context, open mailops.Opener, p MoveBack) (py.Dict, error) {
	var restored, missing int
	err := mailops.Session(ctx, open, p.Account, func(client mail.Client) error {
		current, err := validity(ctx, client, p.DestIMAP)
		if err != nil {
			return err
		}
		if p.DestUIDValidity != nil && current != *p.DestUIDValidity {
			return mailops.Error("destination UIDVALIDITY changed")
		}
		if err := client.Select(ctx, p.DestIMAP); err != nil {
			return err
		}
		var uids []uint32
		for _, entry := range p.Items {
			if entry.DestUID != nil {
				uids = append(uids, *entry.DestUID)
				continue
			}
			var found []uint32
			if entry.MsgID != "" {
				var err error
				if found, err = client.UIDSearch(ctx, mail.SearchCriteria{HeaderName: "Message-ID", HeaderValue: entry.MsgID}); err != nil {
					return err
				}
			}
			if len(found) == 1 {
				uids = append(uids, found[0])
			} else {
				missing++
			}
		}
		size, sizeErr := mailops.BatchSize(ctx, client)
		if sizeErr != nil {
			return sizeErr
		}
		present := map[uint32]bool{}
		for _, chunk := range mailops.Chunks(uids, size) {
			set := mail.UIDs(chunk...)
			found, err := client.UIDSearch(ctx, mail.SearchCriteria{UID: &set})
			if err != nil {
				return err
			}
			for _, u := range found {
				present[u] = true
			}
		}
		var usable []uint32
		for _, u := range uids {
			if present[u] {
				usable = append(usable, u)
			}
		}
		slices.Sort(usable)
		missing += len(uids) - len(usable)
		if len(usable) == 0 {
			return mailops.Error("no message left in the destination to move back")
		}
		return mailops.MoveUIDs(ctx, client, p.DestIMAP, p.SrcIMAP, current, usable, size, func(batch []uint32, _ *mail.CopyUID) error {
			restored += len(batch)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "restored", Value: restored}, {Key: "missing", Value: missing}}, nil
}

func undoGmailLabels(ctx context.Context, open mailops.Opener, p GmailLabels) (py.Dict, error) {
	err := mailops.Session(ctx, open, gmailAccount(p.Account), func(client mail.Client) error {
		v, err := validity(ctx, client, p.IMAPName)
		if err != nil {
			return err
		}
		if v != p.UIDValidity {
			return mailops.Error("UIDVALIDITY changed")
		}
		size, err := mailops.BatchSize(ctx, client)
		if err != nil {
			return err
		}
		return mailops.StoreLabels(ctx, client, p.IMAPName, p.UIDValidity, p.UIDs, size, p.Add, p.Remove)
	})
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "messages", Value: len(p.UIDs)}}, nil
}

func undoGmailUntrash(ctx context.Context, open mailops.Opener, p GmailUntrash) (py.Dict, error) {
	err := mailops.Session(ctx, open, gmailAccount(p.Account), func(client mail.Client) error {
		if p.DestUIDValidity != nil {
			v, err := validity(ctx, client, p.TrashIMAP)
			if err != nil {
				return err
			}
			if v != *p.DestUIDValidity {
				return mailops.Error("Trash UIDVALIDITY changed")
			}
		}
		size, err := mailops.BatchSize(ctx, client)
		if err != nil {
			return err
		}
		return mailops.StoreLabels(ctx, client, p.TrashIMAP, derefOr0(p.DestUIDValidity), p.UIDs, size, nil, []string{mailops.TrashLabel})
	})
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "messages", Value: len(p.UIDs)}}, nil
}

func undoUnflag(ctx context.Context, open mailops.Opener, p Unflag) (py.Dict, error) {
	err := mailops.Session(ctx, open, p.Account, func(client mail.Client) error {
		v, err := validity(ctx, client, p.IMAPName)
		if err != nil {
			return err
		}
		if v != p.UIDValidity {
			return mailops.Error("UIDVALIDITY changed")
		}
		size, err := mailops.BatchSize(ctx, client)
		if err != nil {
			return err
		}
		return mailops.StoreFlag(ctx, client, p.IMAPName, p.UIDValidity, p.UIDs, size, mailops.Flagged, false)
	})
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "messages", Value: len(p.UIDs)}}, nil
}

func derefOr0(v *uint32) uint32 {
	if v == nil {
		return 0
	}
	return *v
}
