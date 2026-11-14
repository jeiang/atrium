package application

import (
	"errors"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
)

type Proposal = proposals.Proposal

func IsLockBusy(err error) bool {
	return errors.As(err, new(*db.LockBusy))
}
