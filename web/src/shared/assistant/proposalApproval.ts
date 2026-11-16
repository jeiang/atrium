import type { ActionPayload, ProposalDetail } from '../../contract';
import type { Approval, ApprovalChange, Arg } from './types';

const audienceLabels = ['to', 'recipient', 'recipients', 'attendees', 'with'];

export interface ApprovalOrigin {
  id: string;
  from: string;
  why: string;
  inRun: boolean;
}

export function proposalToApproval(proposal: ProposalDetail, origin: ApprovalOrigin): Approval {
  const action = proposal.payload.kind === 'action' ? (proposal.payload as ActionPayload) : undefined;
  const change = (action?.change ?? null) as ApprovalChange | null;
  const args: Arg[] = action?.args ?? [];
  const audience = args.find((arg) => audienceLabels.includes(arg.label.toLowerCase()))?.value;
  const label = action?.label || (origin.inRun ? 'Allow this step' : proposal.kind);
  return {
    id: origin.id,
    kind: change?.kind ?? action?.proposal_kind ?? proposal.kind,
    title: proposal.title,
    from: origin.from,
    created: new Date(proposal.created_at * 1000).toISOString(),
    why: origin.why,
    change,
    plain: { label, args, preview: action?.preview ?? null },
    consequence: {
      reversible: proposal.undoable ? 'Yes, for 10 seconds with Undo, then from History' : 'No',
      audience: audience ?? 'Only you',
      cost: 'Free',
      outside: proposal.account
        ? `Changes ${proposal.account}`
        : action
          ? `${label} through ${action.module}`
          : origin.inRun
            ? 'Lets the assistant continue this run'
            : 'Stays inside atrium',
    },
    primary: origin.inRun ? label : 'Approve',
    irreversible: !proposal.undoable,
    done: origin.inRun ? `${label}: done` : 'Approved. atrium applied it',
  };
}
