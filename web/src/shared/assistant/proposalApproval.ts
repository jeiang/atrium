import {
  type ActionPayload,
  ApiError,
  type Effect,
  ok,
  type ProposalDetail,
  reviewEffects,
  reviewUndoEffect,
} from '../../contract';
import { resolutionOf } from './stuck';
import type { Approval, ApprovalChange, Arg } from './types';

const proposalOutcomes: Record<string, string> = {
  approved: 'Approved',
  applied: 'Approved and applied',
  failed: 'Approved, but it failed',
  dismissed: 'Declined',
  suppressed: 'Declined for good',
};

export function proposalOutcome(proposal: ProposalDetail, effects: readonly Effect[] = []): string {
  const resolved = resolutionOf(effects);
  if (resolved) return resolved.outcome;
  if (proposal.undone_at) return 'Approved, then undone';
  return proposalOutcomes[proposal.status] ?? 'Status unknown';
}

export function proposalCanUndo(proposal: ProposalDetail): boolean {
  return proposal.undoable && !proposal.undone_at && proposal.actions.includes('undo');
}

export async function handleUndoProposal(id: number | string): Promise<void> {
  const effects = ok(await reviewEffects({ proposal_id: String(id) }));
  const open = effects.items.filter((effect) => effect.undoable && !effect.undone_at);
  if (open.length === 0)
    throw new ApiError(409, { error: { code: 'not_undoable', message: 'Nothing is left to undo.' } });
  for (const effect of open)
    ok(await reviewUndoEffect(effect.id, { headers: { 'Idempotency-Key': crypto.randomUUID() } }));
}

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
