package proposals

type ProposalError string

func (e ProposalError) Error() string { return string(e) }

type SelectionError string

func (e SelectionError) Error() string { return string(e) }

func (e SelectionError) As(target any) bool {
	if p, ok := target.(*ProposalError); ok {
		*p = ProposalError(e)
		return true
	}
	return false
}
