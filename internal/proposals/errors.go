package proposals

// ProposalError is ProposalError; its text is shown to the operator.
type ProposalError string

func (e ProposalError) Error() string { return string(e) }

func (ProposalError) PyClass() string { return "ProposalError" }

// SelectionError is SelectionError: the operator's selection does not fit the proposal, which stays pending.
// It is a ProposalError as well, so errors.As with a *ProposalError matches it.
type SelectionError string

func (e SelectionError) Error() string { return string(e) }

func (SelectionError) PyClass() string { return "SelectionError" }

func (e SelectionError) As(target any) bool {
	if p, ok := target.(*ProposalError); ok {
		*p = ProposalError(e)
		return true
	}
	return false
}
