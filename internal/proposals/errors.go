package proposals

type ProposalError string

func (e ProposalError) Error() string { return string(e) }

func (ProposalError) PyClass() string { return "ProposalError" }

func (ProposalError) PyModule() string { return "atrium.proposals" }

type SelectionError string

func (e SelectionError) Error() string { return string(e) }

func (SelectionError) PyClass() string { return "SelectionError" }

func (SelectionError) PyModule() string { return "atrium.proposals" }

func (e SelectionError) As(target any) bool {
	if p, ok := target.(*ProposalError); ok {
		*p = ProposalError(e)
		return true
	}
	return false
}
