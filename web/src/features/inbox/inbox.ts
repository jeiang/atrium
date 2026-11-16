import { createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  assistantRunAnswer,
  type Draft,
  mailDraft,
  mailDraftDiscard,
  mailDraftSend,
  ok,
  type ProposalDetail,
  type QueueCounts,
  type QueueItem,
  type QueueItemKind,
  type QueueItemWaitingFor,
  reviewApprove,
  reviewDismiss,
  reviewEffects,
  reviewProposal,
  reviewQueue,
  reviewQueueItem,
  reviewUndoEffect,
} from '../../contract';
import type { InboxId } from '../../shared/assistant/inboxId';
import { proposalToApproval } from '../../shared/assistant/proposalApproval';
import type { Approval, DecidedElsewhere, Decision } from '../../shared/assistant/types';

const QUEUE_TOPICS = [
  'review',
  'effects',
  'mail',
  'attention',
  'assistant',
  'tasks',
  'calendar',
  'ops',
  'settings',
  'connections',
] as const;
const PROPOSAL_TOPICS = ['review', 'effects'] as const;
const DRAFT_TOPICS = ['mail', 'review'] as const;
const QUEUE_PAGE = 100;

export interface InboxRow {
  key: string;
  type: string;
  id: string;
  kind: QueueItemKind;
  title: string;
  label: string;
  summary: string;
  created: number;
  due?: number;
  waitingFor: QueueItemWaitingFor;
  module: string;
  blocked: boolean;
}

export interface Queue {
  rows: InboxRow[];
  total: number;
  counts: QueueCounts;
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

const waitingWords: Record<QueueItemWaitingFor, string> = {
  answer: 'Question',
  permission: 'Permission',
  approval: 'Approval',
  send: 'Email',
  resolve: 'Stuck',
  review: 'Review',
  due: 'Due',
};

export function queueRow(item: QueueItem): InboxRow {
  return {
    key: item.id,
    type: item.source.type,
    id: item.source.id,
    kind: item.kind,
    title: item.title || '(no title)',
    label: waitingWords[item.waiting_for],
    summary: item.summary,
    created: item.arrived_at,
    due: item.due ?? undefined,
    waitingFor: item.waiting_for,
    module: item.module,
    blocked: item.waiting_for === 'resolve',
  };
}

export function useQueue() {
  return createQuery(() => ({
    queryKey: ['inbox', 'queue'],
    queryFn: async ({ signal }): Promise<Queue> => {
      let page = ok(await reviewQueue({ limit: QUEUE_PAGE }, { signal })).data;
      const items = [...page.items];
      while (page.next_cursor) {
        page = ok(await reviewQueue({ limit: QUEUE_PAGE, cursor: page.next_cursor }, { signal })).data;
        items.push(...page.items);
      }
      return { rows: items.map(queueRow), total: page.total, counts: page.counts };
    },
    meta: { topics: QUEUE_TOPICS },
  }));
}

export function useQueueItem(item: () => InboxId) {
  return createQuery(() => {
    const key = inboxKey(item());
    return {
      queryKey: ['inbox', 'queued', key],
      queryFn: async ({ signal }): Promise<QueueItem | null> => {
        try {
          return ok(await reviewQueueItem(key, { signal })).data;
        } catch (error) {
          if (error instanceof ApiError && error.status === 404) return null;
          throw error;
        }
      },
      meta: { topics: QUEUE_TOPICS },
    };
  });
}

export async function handleAnswer(threadId: number, text: string): Promise<void> {
  ok(await assistantRunAnswer(threadId, { text }, oneGesture()));
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
  const decidedAt = detail.decided_at ?? detail.applied_at ?? detail.created_at;
  return {
    approval: proposalToApproval(detail, {
      id: key,
      from: action?.module ? `atrium, ${action.module}` : 'atrium',
      why: '',
      inRun: false,
    }),
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
    const { type } = item();
    const id = Number(item().id);
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
    const sent = ok(await mailDraftSend(Number(item.id), revision === undefined ? {} : { revision }, oneGesture()));
    return sent.refused.length > 0 ? `The server refused ${sent.refused.join(', ')}.` : undefined;
  }
  const done = ok(await reviewApprove(Number(item.id), {}, oneGesture()));
  return done.outcome === 'applied' ? undefined : (done.error ?? 'atrium could not apply it.');
}

export async function handleDecline(item: InboxId): Promise<undefined> {
  if (item.type === 'draft') ok(await mailDraftDiscard(Number(item.id), oneGesture()));
  else ok(await reviewDismiss(Number(item.id), {}, oneGesture()));
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
