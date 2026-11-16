import { createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  type Approval as ApprovalRow,
  type Draft,
  mailDraft,
  mailDraftDiscard,
  mailDraftSend,
  ok,
  type ProposalDetail,
  reviewApprove,
  reviewDismiss,
  reviewEffects,
  reviewPending,
  reviewProposal,
  reviewUndoEffect,
} from '../../contract';
import type { InboxId, InboxType } from '../../shared/assistant/inboxId';
import type { Approval, ApprovalChange, DecidedElsewhere, Decision } from '../../shared/assistant/types';

const PENDING_TOPICS = ['review', 'effects', 'mail'] as const;
const PROPOSAL_TOPICS = ['review', 'effects'] as const;
const DRAFT_TOPICS = ['mail', 'review'] as const;

export interface InboxRow {
  key: string;
  type: InboxType;
  id: number;
  title: string;
  kind: string;
  created: number;
  blocked: boolean;
}

export interface InboxItem {
  approval: Approval;
  revision?: number;
  decided?: DecidedElsewhere;
  blocked: boolean;
  undoable: readonly Decision[];
}

export function inboxKey(item: InboxId): string {
  return `${item.type}:${item.id}`;
}

function row(type: InboxType, id: number, title: string, kind: string, created: number, blocked: boolean): InboxRow {
  return { key: `${type}:${id}`, type, id, title, kind, created, blocked };
}

function proposalRow(item: ApprovalRow, blocked: boolean): InboxRow {
  return row('proposal', item.id, item.title, item.kind_label, item.created_at, blocked);
}

export function usePending() {
  return createQuery(() => ({
    queryKey: ['inbox', 'pending'],
    queryFn: async ({ signal }) => {
      const body = ok(await reviewPending({ signal }));
      const rows = [
        ...body.approvals.map((item) => proposalRow(item, false)),
        ...body.drafts.map((draft) =>
          row('draft', draft.id, draft.subject || '(no subject)', 'Email', draft.created_at, false),
        ),
        ...body.blocked.map((item) => proposalRow(item, true)),
      ];
      return rows.sort((a, b) => Number(a.blocked) - Number(b.blocked) || a.created - b.created);
    },
    meta: { topics: PENDING_TOPICS },
  }));
}

function iso(seconds: number): string {
  return new Date(seconds * 1000).toISOString();
}

function firstName(to: string): string {
  const first = to.split(',')[0]?.trim() ?? '';
  const name = first
    .replace(/<[^>]*>/, '')
    .replace(/"/g, '')
    .trim();
  return (name.includes('@') ? name.split('@')[0] : name.split(' ')[0]) || 'the recipient';
}

const proposalOutcomes: Record<string, string> = {
  approved: 'Approved',
  applied: 'Approved and applied',
  failed: 'Approved, but it failed',
  dismissed: 'Declined',
  suppressed: 'Declined for good',
};

const draftOutcomes: Record<string, string> = {
  sending: 'Sending started',
  sent: 'Sent',
  discarded: 'Discarded',
  unknown: 'Sent with an unknown result',
  invalid: 'Refused as invalid',
};

function proposalItem(key: string, detail: ProposalDetail): InboxItem {
  const action = detail.payload.kind === 'action' ? detail.payload : undefined;
  const change = (action?.change ?? null) as ApprovalChange | null;
  const decidedAt = detail.decided_at ?? detail.applied_at ?? detail.created_at;
  return {
    approval: {
      id: key,
      kind: change?.kind ?? action?.proposal_kind ?? detail.kind,
      title: detail.title,
      from: action?.module ? `atrium, ${action.module}` : 'atrium',
      created: iso(detail.created_at),
      why: '',
      change,
      plain: { label: action?.label ?? detail.kind, args: action?.args ?? [], preview: action?.preview ?? null },
      consequence: {
        reversible: detail.undoable ? 'Yes, for 10 seconds with Undo, then from History' : 'No',
        audience: 'Only you',
        cost: 'Free',
        outside: detail.account ? `Changes ${detail.account}` : 'Stays inside atrium',
      },
      primary: 'Approve',
      irreversible: !detail.undoable,
      done: 'Approved. atrium applied it',
    },
    decided:
      detail.status === 'pending'
        ? undefined
        : {
            outcome: detail.undone_at
              ? 'Approved, then undone'
              : (proposalOutcomes[detail.status] ?? `Marked ${detail.status}`),
            at: iso(detail.undone_at ?? decidedAt),
          },
    blocked: detail.blocked,
    undoable: detail.undoable ? ['approve'] : [],
  };
}

function draftItem(key: string, draft: Draft): InboxItem {
  const name = firstName(draft.to);
  return {
    approval: {
      id: key,
      kind: 'mail_draft',
      title: `Send email to ${name}: ${draft.subject || '(no subject)'}`,
      from: 'atrium, mail',
      created: iso(draft.created_at),
      why: '',
      change: {
        kind: 'mail_draft',
        account: draft.account,
        to: draft.to,
        cc: draft.cc,
        subject: draft.subject,
        body: draft.body,
        unexpected_recipients: draft.unexpected_recipients,
      },
      plain: { label: 'Email', args: [], preview: null },
      consequence: {
        reversible: 'No. Mail cannot be recalled once sent',
        audience: draft.cc ? `${draft.to}, ${draft.cc}` : draft.to,
        cost: 'Free',
        outside: `Sends from ${draft.account}`,
      },
      primary: `Send email to ${name}`,
      irreversible: true,
      done: `Sent to ${draft.to}`,
      sending: 'Sending',
    },
    revision: draft.revision,
    decided:
      draft.status === 'draft'
        ? undefined
        : {
            outcome: draftOutcomes[draft.status] ?? `Marked ${draft.status}`,
            at: iso(draft.sent_at ?? draft.updated_at),
          },
    blocked: !draft.account_configured,
    undoable: [],
  };
}

export function useInboxItem(item: () => InboxId) {
  return createQuery(() => {
    const { type, id } = item();
    const key = `${type}:${id}`;
    return {
      queryKey: ['inbox', 'item', key],
      queryFn: async ({ signal }): Promise<InboxItem | null> => {
        try {
          if (type === 'proposal') return proposalItem(key, ok(await reviewProposal(id, { signal })).data);
          if (type === 'draft') return draftItem(key, ok(await mailDraft(id, { signal })));
          return null;
        } catch (error) {
          if (error instanceof ApiError && error.status === 404) return null;
          throw error;
        }
      },
      meta: { topics: type === 'draft' ? DRAFT_TOPICS : PROPOSAL_TOPICS },
    };
  });
}

function oneGesture(): RequestInit {
  return { headers: { 'Idempotency-Key': crypto.randomUUID() } };
}

export async function handleApprove(item: InboxId, revision: number | undefined): Promise<string | undefined> {
  if (item.type === 'draft') {
    const sent = ok(await mailDraftSend(item.id, revision === undefined ? {} : { revision }, oneGesture()));
    return sent.refused.length > 0 ? `The server refused ${sent.refused.join(', ')}.` : undefined;
  }
  const done = ok(await reviewApprove(item.id, {}, oneGesture()));
  return done.outcome === 'applied' ? undefined : (done.error ?? 'atrium could not apply it.');
}

export async function handleDecline(item: InboxId): Promise<undefined> {
  if (item.type === 'draft') ok(await mailDraftDiscard(item.id, oneGesture()));
  else ok(await reviewDismiss(item.id, {}, oneGesture()));
}

export async function handleUndo(item: InboxId): Promise<void> {
  const effects = ok(await reviewEffects({ proposal_id: String(item.id) }));
  const open = effects.items.filter((effect) => effect.undoable && !effect.undone_at);
  if (open.length === 0)
    throw new ApiError(409, { error: { code: 'not_undoable', message: 'Nothing is left to undo.' } });
  for (const effect of open) ok(await reviewUndoEffect(effect.id, oneGesture()));
}

export function refreshInbox(client: QueryClient): void {
  void client.invalidateQueries({ queryKey: ['inbox'] });
  void client.invalidateQueries({ queryKey: ['shell'] });
}
