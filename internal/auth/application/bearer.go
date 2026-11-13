package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/auth/bearer"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	BearerListName   application.Name = "auth.bearer-list"
	BearerIssueName  application.Name = "auth.bearer-issue"
	BearerRevokeName application.Name = "auth.bearer-revoke"
)

type Token struct {
	ID         int64      `json:"id"`
	Kind       string     `json:"kind"`
	Name       string     `json:"name"`
	Subject    string     `json:"subject"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

type BearerIssueInput struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type Issued struct {
	Token  Token  `json:"token"`
	Secret string `json:"secret"`
}

type BearerRevokeInput struct {
	ID int64 `json:"id"`
}

func DescribeBearerList() application.Definition {
	return application.Describe[struct{}, []Token](BearerListName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeBearerIssue() application.Definition {
	return application.Describe[BearerIssueInput, Issued](BearerIssueName, application.Command, []application.Authority{application.Operator}, application.LocalUndoable, true)
}

func DescribeBearerRevoke() application.Definition {
	return application.Describe[BearerRevokeInput, struct{}](BearerRevokeName, application.Command, []application.Authority{application.Operator}, application.LocalUndoable, true)
}

func (s *Service) BearerList(ctx context.Context) (application.Read[[]Token], error) {
	rev, err := s.revision(ctx, BearerListName)
	if err != nil {
		return application.Read[[]Token]{}, err
	}
	tokens, err := s.bearers.List(ctx)
	if err != nil {
		return application.Read[[]Token]{}, err
	}
	out := make([]Token, len(tokens))
	for i, tok := range tokens {
		out[i] = token(tok)
	}
	return application.Read[[]Token]{Data: out, Revision: rev}, nil
}

func (s *Service) BearerIssue(ctx context.Context, in BearerIssueInput) (application.Change[Issued], error) {
	var auditID int64
	issued, err := s.bearers.Issue(ctx, in.Name, in.ExpiresAt, func(ctx context.Context, tx *sql.Tx, tok bearer.Token) error {
		var err error
		if err := history(ctx, tx, "auth_token.issue", tok); err != nil {
			return err
		}
		auditID, err = s.audit(ctx, tx, BearerIssueName, state.OK, tok)
		return err
	})
	var message string
	switch {
	case errors.Is(err, bearer.ErrName):
		message = "A name of at most 100 characters is required."
	case errors.Is(err, bearer.ErrExpiry):
		message = "The expiry must be in the future."
	case err != nil:
		return application.Change[Issued]{}, err
	default:
		return application.Change[Issued]{Data: Issued{Token: token(issued.Token), Secret: issued.Secret}, AuditID: auditID}, nil
	}
	refusal := &application.Error{Code: application.Invalid, Message: message, Cause: err}
	if _, err := s.audit(ctx, nil, BearerIssueName, state.Invalid, bearer.Token{}); err != nil {
		return application.Change[Issued]{}, errors.Join(refusal, err)
	}
	return application.Change[Issued]{}, refusal
}

func (s *Service) BearerRevoke(ctx context.Context, in BearerRevokeInput) (application.Change[struct{}], error) {
	var auditID int64
	err := s.bearers.Revoke(ctx, in.ID, func(ctx context.Context, tx *sql.Tx, tok bearer.Token, changed bool) error {
		outcome := state.OK
		if !changed {
			outcome = state.Noop
		}
		if err := history(ctx, tx, "auth_token.revoke", tok); err != nil {
			return err
		}
		var err error
		auditID, err = s.audit(ctx, tx, BearerRevokeName, outcome, bearer.Token{ID: in.ID})
		return err
	})
	if !errors.Is(err, bearer.ErrNotFound) {
		return application.Change[struct{}]{AuditID: auditID}, err
	}
	missing := &application.Error{Code: application.NotFound, Message: "no token " + strconv.FormatInt(in.ID, 10), Cause: err}
	if _, err := s.audit(ctx, nil, BearerRevokeName, state.NotFound, bearer.Token{ID: in.ID}); err != nil {
		return application.Change[struct{}]{}, errors.Join(missing, err)
	}
	return application.Change[struct{}]{}, missing
}

func (s *Service) audit(ctx context.Context, tx *sql.Tx, name application.Name, outcome state.Outcome, tok bearer.Token) (int64, error) {
	details := map[string]any{}
	target := ""
	if tok.ID != 0 {
		details["token_id"] = tok.ID
		target = strconv.FormatInt(tok.ID, 10)
	}
	if tok.Kind != "" {
		details["kind"] = tok.Kind
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return 0, err
	}
	return s.state.Audit(ctx, tx, state.Entry{Operation: string(name), Target: target, DetailsJSON: string(raw), Outcome: outcome})
}

func history(ctx context.Context, tx *sql.Tx, key string, tok bearer.Token) error {
	value := fmt.Sprintf("id=%d name=%s kind=%s", tok.ID, tok.Name, tok.Kind)
	return audit.Record(ctx, tx, audit.Entry{Key: key, New: &value})
}

func token(tok bearer.Token) Token {
	return Token{ID: tok.ID, Kind: tok.Kind, Name: tok.Name, Subject: tok.Subject, CreatedAt: tok.CreatedAt, ExpiresAt: tok.ExpiresAt, LastUsedAt: tok.LastUsedAt, RevokedAt: tok.RevokedAt}
}
