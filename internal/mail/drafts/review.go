package drafts

import (
	"cmp"
	"context"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/review"
)

const ReviewLimit = 100

func ReviewDraft(row Row, unexpected []string) review.Draft {
	return review.Draft{
		ID: row.ID, ThreadID: row.ThreadID, Account: row.Account, ToAddrs: row.ToAddrs, CcAddrs: row.CcAddrs, Subject: row.Subject,
		Body: row.Body, InReplyToMessageID: row.InReplyToMessageID, Status: string(row.Status), Unknown: row.Status == Unknown,
		Revision: row.Revision, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, SentAt: row.SentAt, SMTPResult: row.SMTPResult,
		Unexpected: unexpected,
	}
}

func (s *Store) ReviewDrafts(ctx context.Context, take func(int) int) ([]review.Draft, error) {
	open, err := s.List(ctx, Draft, ReviewLimit)
	if err != nil {
		return nil, err
	}
	unknown, err := s.List(ctx, Unknown, ReviewLimit)
	if err != nil {
		return nil, err
	}
	rows := append(unknown, open...)
	slices.SortStableFunc(rows, func(a, b Row) int { return cmp.Compare(b.UpdatedAt, a.UpdatedAt) })
	rows = rows[:take(len(rows))]
	unexpected, err := s.UnexpectedRecipientsBatch(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := make([]review.Draft, len(rows))
	for i, row := range rows {
		out[i] = ReviewDraft(row, unexpected[i])
	}
	return out, nil
}

func ReviewNeeds(ctx context.Context, q db.Querier) ([]review.Need, error) {
	needs, err := Needs(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]review.Need, len(needs))
	for i, n := range needs {
		out[i] = review.Need{ID: n.ID, Subject: n.Subject, To: n.To, Unknown: n.Status == Unknown, CreatedAt: n.CreatedAt}
	}
	return out, nil
}
