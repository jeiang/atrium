import { createInfiniteQuery, createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  type Draft,
  type DraftCreateRequest,
  type DraftUpdateRequest,
  type Account as MailAccount,
  type MailMessage,
  type MailSearchHit,
  type MessageActionResult,
  mailAccounts,
  mailCorrect,
  mailDraftCreate,
  mailDraftDiscard,
  mailDraftForward,
  mailDraftReply,
  mailDraftResolve,
  mailDraftSend,
  mailDraftUpdate,
  mailMessage,
  mailMessageArchive,
  mailMessageFlag,
  mailMessageRead,
  mailOpenLoops,
  mailSearch,
  ok,
  reviewQueueItem,
  reviewResolveEffect,
  reviewUndo,
} from '../../contract';
import { outcomeUnknown, refusedOffline } from '../../shared/device.svelte';

export const MAIL_TOPICS = ['mail', 'effects'] as const;
const QUEUE_TOPICS = ['mail', 'attention', 'review'] as const;
const PAGE = 30;

export type { Draft, MailAccount, MailMessage, MailSearchHit };

export interface Filter {
  q: string;
  account: string;
  folder: string;
}

export function useAccounts(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['mail', 'accounts'],
    queryFn: async ({ signal }) => ok(await mailAccounts({ limit: 100 }, { signal })).data.items,
    enabled: enabled(),
    meta: { topics: ['mail', 'settings', 'connections'], persist: false },
  }));
}

export function useMessages(filter: () => Filter, enabled: () => boolean) {
  return createInfiniteQuery(() => {
    const { q, account, folder } = filter();
    return {
      queryKey: ['mail', 'messages', q, account, folder],
      queryFn: async ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) =>
        ok(
          await mailSearch(
            {
              q: q || undefined,
              account: account || undefined,
              folder: folder || undefined,
              limit: PAGE,
              cursor: pageParam || undefined,
            },
            { signal },
          ),
        ).data,
      initialPageParam: '',
      getNextPageParam: (last: { next_cursor?: string | null }) => last.next_cursor || undefined,
      enabled: enabled(),
      meta: { topics: MAIL_TOPICS, persist: false },
    };
  });
}

export function useMessage(id: () => number | undefined) {
  return createQuery(() => {
    const target = id();
    return {
      queryKey: ['mail', 'message', target],
      queryFn: async ({ signal }): Promise<MailMessage | null> => {
        try {
          return ok(await mailMessage(target as number, undefined, { signal })).data;
        } catch (error) {
          if (error instanceof ApiError && error.status === 404) return null;
          throw error;
        }
      },
      enabled: target !== undefined,
      meta: { topics: MAIL_TOPICS, persist: false },
    };
  });
}

export function useAccountFolders(account: () => string, enabled: () => boolean) {
  return createQuery(() => {
    const target = account();
    return {
      queryKey: ['mail', 'folders', target],
      queryFn: async ({ signal }) =>
        ok(await mailSearch({ account: target, folders: true, limit: 1 }, { signal })).data.folders ?? [],
      enabled: target !== '' && enabled(),
      meta: { topics: MAIL_TOPICS, persist: false },
    };
  });
}

export interface Corrected {
  text: string;
  settled: boolean;
  effect?: number;
}

export async function handleCorrect(id: number, folder: string): Promise<Corrected> {
  const change = ok(await mailCorrect(id, { folder, mode: 'immediate' }));
  const { correction, note, outcome } = change.data;
  return {
    text: `Corrected to ${correction.folder}.${note}`,
    settled: outcome === 'ok' || outcome === 'deferred',
    effect: change.undoable === false ? undefined : change.effect_ids?.[0],
  };
}

export function useQueueLink(id: () => number | undefined) {
  return createQuery(() => {
    const target = id();
    return {
      queryKey: ['mail', 'queued', target],
      queryFn: async ({ signal }): Promise<string | null> => {
        const loops = ok(await mailOpenLoops({ limit: 100 }, { signal })).data.items;
        const loop = loops.find((item) => item.last_message_id === target && !item.dismissed);
        if (!loop) return null;
        try {
          return ok(await reviewQueueItem(`loop:${loop.thread_key}`, { signal })).data.id;
        } catch (error) {
          if (error instanceof ApiError && error.status === 404) return null;
          throw error;
        }
      },
      enabled: target !== undefined,
      meta: { topics: QUEUE_TOPICS, persist: false },
    };
  });
}

export interface Done {
  result: MessageActionResult;
  effect?: number;
}

function done(change: { data: MessageActionResult; effect_ids?: number[]; undoable?: boolean }): Done {
  return { result: change.data, effect: change.undoable === false ? undefined : change.effect_ids?.[0] };
}

export async function handleArchive(id: number): Promise<Done> {
  return done(ok(await mailMessageArchive(id)));
}

export async function handleRead(id: number, read: boolean): Promise<Done> {
  return done(ok(await mailMessageRead(id, { read })));
}

export async function handleFlag(id: number, flagged: boolean): Promise<Done> {
  return done(ok(await mailMessageFlag(id, { flagged })));
}

export async function handleUndo(effect: number): Promise<void> {
  const undone = ok(await reviewUndo(`write:${effect}`));
  if (!undone.undone)
    throw new ApiError(409, { error: { code: 'not_undoable', message: undone.error || 'Nothing is left to undo.' } });
}

export async function handleResolveEffect(effect: number, outcome: string): Promise<void> {
  ok(await reviewResolveEffect(effect, { outcome }));
}

export async function handleReply(id: number): Promise<Draft> {
  return ok(await mailDraftReply(id, {}));
}

export async function handleForward(id: number, to: string, cc: string): Promise<Draft> {
  return ok(await mailDraftForward(id, cc ? { to, cc } : { to }));
}

export async function handleCreate(input: DraftCreateRequest): Promise<Draft> {
  return ok(await mailDraftCreate(input));
}

export async function handleSave(id: number, fields: DraftUpdateRequest): Promise<Draft> {
  return ok(await mailDraftUpdate(id, fields));
}

export async function handleSend(id: number, revision: number): Promise<{ draft: Draft; refused: string[] }> {
  const sent = ok(await mailDraftSend(id, { revision }));
  return { draft: sent.draft, refused: sent.refused };
}

export async function handleDiscard(id: number): Promise<void> {
  ok(await mailDraftDiscard(id));
}

export async function handleResolveDraft(id: number, outcome: 'sent' | 'draft'): Promise<Draft> {
  return ok(await mailDraftResolve(id, { outcome })).draft;
}

export function refreshMail(client: QueryClient): void {
  void client.invalidateQueries({ queryKey: ['mail'] });
  void client.invalidateQueries({ queryKey: ['inbox'] });
}

const UNKNOWN = new Set(['write_outcome_unknown', 'write_partial', 'send_outcome_unknown']);

export function unknownOutcome(error: unknown): error is ApiError {
  return outcomeUnknown(error) || (error instanceof ApiError && UNKNOWN.has(error.code));
}

export function effectOf(error: unknown): number | undefined {
  if (!(error instanceof ApiError)) return undefined;
  const body = error.body as { error?: { effect_ids?: number[] } } | undefined;
  return body?.error?.effect_ids?.[0];
}

export interface Problem {
  tone: 'warn' | 'error';
  title: string;
  text: string;
}

const refusals: Record<string, { title: string; text: string }> = {
  operator_only: { title: 'Only you can change this message', text: 'atrium changes messages only for the operator.' },
  shadow_mode: {
    title: 'Mail is in shadow mode',
    text: 'atrium only watches the mailbox in shadow mode. Switch Mail to live mode to change messages.',
  },
  no_archive_folder: {
    title: 'This account has no archive folder',
    text: 'Add an archive folder on the mail server, then archive again.',
  },
  protected_folder: { title: 'This folder is protected', text: 'atrium does not archive from this folder.' },
  already_archived: { title: 'Already archived', text: 'This message is already in the archive.' },
  not_in_inbox: { title: 'Not in the inbox', text: 'Only messages in the inbox can be archived.' },
  message_changed: {
    title: 'The message changed on the server',
    text: 'It moved or changed since it loaded. atrium reloaded it.',
  },
  write_blocked: {
    title: 'An earlier change is unresolved',
    text: 'atrium could not confirm an earlier change to this message. Record what happened to it first.',
  },
  invalid_draft: { title: 'The draft is not complete', text: 'Check the recipients and the message.' },
  stale: {
    title: 'The draft changed elsewhere',
    text: 'atrium reloaded the latest version. Check it, then try again.',
  },
  not_sent: { title: 'The email was not sent', text: 'The mail server refused it.' },
};

export function problemOf(error: unknown): Problem {
  if (refusedOffline(error) && error instanceof Error) {
    return { tone: 'warn', title: 'Cannot reach atrium', text: `${error.message} Nothing was sent.` };
  }
  if (error instanceof ApiError) {
    const known = refusals[error.code];
    if (known) return { tone: 'warn', title: known.title, text: `${known.text} Nothing was changed.` };
    if (error.status === 404) return { tone: 'warn', title: 'This is gone', text: error.message };
    return { tone: 'error', title: 'atrium refused this', text: `${error.message} Nothing was changed.` };
  }
  return {
    tone: 'error',
    title: 'The outcome is unknown',
    text: 'The connection failed before atrium answered. atrium did not try again.',
  };
}

export function sender(from: string): string {
  const match = /^\s*"?([^"<]*?)"?\s*<[^>]+>\s*$/.exec(from);
  return match?.[1] || from;
}

export function firstName(to: string): string {
  const first = to.split(',')[0]?.trim() ?? '';
  return sender(first) || first || 'nobody';
}

export function sizeText(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${Math.round(size / 1024)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}
