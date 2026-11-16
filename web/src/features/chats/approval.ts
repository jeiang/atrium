import type { ActionPayload, ProposalDetail } from '../../contract';
import type { Approval, ApprovalChange, Arg } from '../../shared/assistant/types';

const audienceLabels = ['to', 'recipient', 'recipients', 'attendees', 'with'];

export function toApproval(proposal: ProposalDetail, threadTitle: string): Approval {
  const payload = proposal.payload;
  const action = payload.kind === 'action' ? (payload as ActionPayload) : undefined;
  const args: Arg[] = action?.args ?? [];
  const audience = args.find((arg) => audienceLabels.includes(arg.label.toLowerCase()))?.value;
  const label = action?.label || 'Allow this step';
  const created = new Date(proposal.created_at * 1000).toISOString();
  return {
    id: String(proposal.id),
    kind: action?.proposal_kind ?? proposal.kind,
    title: proposal.title,
    from: threadTitle,
    created,
    why: 'atrium paused this chat to ask before it acts.',
    change: (action?.change as ApprovalChange | null | undefined) ?? null,
    plain: { label, args, preview: action?.preview ?? null },
    consequence: {
      reversible: proposal.undoable ? 'Yes, for a while after it runs' : 'No. It cannot be undone once done',
      audience: audience ?? 'Only you',
      cost: 'Free',
      outside: action ? `${label} through ${action.module}` : 'Lets the assistant continue this run',
    },
    primary: label,
    irreversible: !proposal.undoable,
    done: `${label}: done`,
  };
}
