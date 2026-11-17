import { createInfiniteQuery, createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  type CommitmentItem,
  type CommitmentPage,
  commitmentsDismiss,
  commitmentsDone,
  commitmentsGet,
  commitmentsList,
  commitmentsReopen,
  ok,
  reviewUndo,
  type Task,
  type TaskAddRequest,
  type TaskLink,
  type TaskPage,
  type TaskUpdateRequest,
  tasksAdd,
  tasksDelete,
  tasksDismiss,
  tasksDone,
  tasksGet,
  tasksList,
  tasksReopen,
  tasksUpdate,
} from '../../contract';
import { formatDay } from '../../shared/assistant/format';
import { inboxPath } from '../../shared/assistant/inboxId';
import { outcomeUnknown, refusedOffline } from '../../shared/device.svelte';
import { toast } from '../../shared/ui/toast.svelte';
import { p } from '../../shell/router';

export const COMMITMENT_TOPICS = ['tasks', 'mail'] as const;
export const TASK_TOPICS = ['tasks'] as const;
const PAGE = 50;

export type Side = 'todo' | 'waiting';
export type Status = 'open' | 'done' | 'dismissed';
export type Verb = 'done' | 'dismiss' | 'reopen';
export type Bucket = 'overdue' | 'today' | 'week' | 'later' | 'none';

export const statuses: readonly Status[] = ['open', 'done', 'dismissed'];
export const statusWords: Record<Status, string> = { open: 'Open', done: 'Done', dismissed: 'Dismissed' };
export const bucketWords: Record<Bucket, string> = {
  overdue: 'Overdue',
  today: 'Today',
  week: 'This week',
  later: 'Later',
  none: 'No date',
};
export const buckets: readonly Bucket[] = ['overdue', 'today', 'week', 'later', 'none'];

export interface Problem {
  tone: 'warn' | 'error';
  title: string;
  text: string;
}

export function useCommitments(filter: () => { side: Side; status: Status; q: string }) {
  return createInfiniteQuery(() => {
    const { side, status, q } = filter();
    return {
      queryKey: ['plan', 'commitments', side, status, q],
      queryFn: async ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) =>
        ok(
          await commitmentsList(
            { side, status, q: q || undefined, cursor: pageParam || undefined, limit: PAGE },
            { signal },
          ),
        ).data,
      initialPageParam: '',
      getNextPageParam: (last: CommitmentPage) => last.next_cursor ?? undefined,
      meta: { topics: COMMITMENT_TOPICS },
    };
  });
}

export function useCommitment(ref: () => string | undefined) {
  return createQuery(() => {
    const target = ref();
    return {
      queryKey: ['plan', 'commitment', target],
      queryFn: async ({ signal }): Promise<CommitmentItem | null> => {
        try {
          return ok(await commitmentsGet(target as string, { signal })).data.item;
        } catch (error) {
          if (error instanceof ApiError && error.status === 404) return null;
          throw error;
        }
      },
      enabled: target !== undefined,
      meta: { topics: COMMITMENT_TOPICS },
    };
  });
}

export function useTasks(filter: () => { status: Status; q: string }) {
  return createInfiniteQuery(() => {
    const { status, q } = filter();
    return {
      queryKey: ['plan', 'tasks', status, q],
      queryFn: async ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) =>
        ok(await tasksList({ status, q: q || undefined, cursor: pageParam || undefined, limit: PAGE }, { signal }))
          .data,
      initialPageParam: '',
      getNextPageParam: (last: TaskPage) => last.next_cursor ?? undefined,
      meta: { topics: TASK_TOPICS },
    };
  });
}

export function useTask(id: () => number | undefined) {
  return createQuery(() => {
    const target = id();
    return {
      queryKey: ['plan', 'task', target],
      queryFn: async ({ signal }): Promise<Task | null> => {
        try {
          return ok(await tasksGet(target as number, { signal })).data.task;
        } catch (error) {
          if (error instanceof ApiError && error.status === 404) return null;
          throw error;
        }
      },
      enabled: target !== undefined,
      meta: { topics: TASK_TOPICS },
    };
  });
}

function oneGesture(): RequestInit {
  return { headers: { 'Idempotency-Key': crypto.randomUUID() } };
}

function effectsOf(change: { effect_ids?: number[]; undoable?: boolean }): number[] {
  return change.undoable === false ? [] : (change.effect_ids ?? []);
}

export async function handleCommitmentVerb(ref: string, verb: Verb): Promise<number[]> {
  if (verb === 'done') return effectsOf(ok(await commitmentsDone(ref, oneGesture())));
  if (verb === 'dismiss') return effectsOf(ok(await commitmentsDismiss(ref, oneGesture())));
  return effectsOf(ok(await commitmentsReopen(ref, oneGesture())));
}

export async function handleTaskVerb(id: number, verb: Verb | 'delete'): Promise<number[]> {
  if (verb === 'done') return effectsOf(ok(await tasksDone(id, oneGesture())));
  if (verb === 'dismiss') return effectsOf(ok(await tasksDismiss(id, oneGesture())));
  if (verb === 'reopen') return effectsOf(ok(await tasksReopen(id, oneGesture())));
  return effectsOf(ok(await tasksDelete(id, oneGesture())));
}

export async function handleTaskAdd(body: TaskAddRequest): Promise<{ id: number; effects: number[] }> {
  const change = ok(await tasksAdd(body, oneGesture()));
  return { id: change.data.task_id, effects: effectsOf(change) };
}

export async function handleTaskUpdate(id: number, body: TaskUpdateRequest): Promise<number[]> {
  return effectsOf(ok(await tasksUpdate(id, body, oneGesture())));
}

export async function handleUndoEffects(effects: readonly number[]): Promise<void> {
  for (const effect of effects) {
    const undone = ok(await reviewUndo(`write:${effect}`, oneGesture()));
    if (!undone.undone) throw new ApiError(409, { error: { code: 'not_undone', message: undone.error ?? '' } });
  }
}

export function refreshPlan(client: QueryClient): void {
  void client.invalidateQueries({ queryKey: ['plan'] });
  void client.invalidateQueries({ queryKey: ['inbox'] });
  void client.invalidateQueries({ queryKey: ['shell'] });
}

export function problemOf(error: unknown): Problem {
  if (refusedOffline(error) && error instanceof Error) {
    return { tone: 'warn', title: 'Cannot reach atrium', text: error.message };
  }
  if (outcomeUnknown(error)) {
    return {
      tone: 'error',
      title: 'The outcome is unknown',
      text: `${error.message} atrium reloaded the list and did not try again.`,
    };
  }
  if (error instanceof ApiError && error.code === 'read_only') {
    return {
      tone: 'warn',
      title: 'This comes from mail',
      text: 'A commitment found in mail can be marked done, dismissed or reopened, but not edited or deleted.',
    };
  }
  if (error instanceof ApiError && (error.status === 409 || error.status === 412)) {
    return {
      tone: 'warn',
      title: 'This changed since it loaded',
      text: 'atrium reloaded it. Check it again before you act.',
    };
  }
  if (error instanceof ApiError) {
    return { tone: 'error', title: 'atrium refused this', text: `${error.message} Nothing was changed.` };
  }
  return {
    tone: 'error',
    title: 'The outcome is unknown',
    text: 'The connection failed before atrium answered, so this may or may not have happened. atrium reloaded the list and did not try again.',
  };
}

async function onUndoToast(effects: readonly number[], client: QueryClient): Promise<void> {
  try {
    await handleUndoEffects(effects);
    toast('Undone.');
  } catch (error) {
    const problem = problemOf(error);
    toast(`${problem.title}. ${problem.text}`, { tone: 'error' });
  } finally {
    refreshPlan(client);
  }
}

export function doneToast(message: string, effects: readonly number[], client: QueryClient): void {
  toast(message, effects.length > 0 ? { action: { label: 'Undo', run: () => void onUndoToast(effects, client) } } : {});
}

export const verbWords: Record<Verb | 'delete', { label: string; done: string }> = {
  done: { label: 'Done', done: 'Marked done' },
  dismiss: { label: 'Dismiss', done: 'Dismissed' },
  reopen: { label: 'Reopen', done: 'Reopened' },
  delete: { label: 'Delete', done: 'Deleted' },
};

function localKey(date: Date): string {
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  return `${date.getFullYear()}-${month}-${day}`;
}

export function todayKey(): string {
  return localKey(new Date());
}

function dayKey(due: string | number): string {
  return typeof due === 'number' ? localKey(new Date(due * 1000)) : due.slice(0, 10);
}

export function bucketOf(due: string | number | null | undefined, today = todayKey()): Bucket {
  if (due === null || due === undefined || due === '') return 'none';
  const key = dayKey(due);
  if (key < today) return 'overdue';
  if (key === today) return 'today';
  const week = new Date(`${today}T12:00:00`);
  week.setDate(week.getDate() + 6);
  return key <= localKey(week) ? 'week' : 'later';
}

export function dueText(due: string | number | null | undefined): string | undefined {
  if (due === null || due === undefined || due === '') return undefined;
  const bucket = bucketOf(due);
  const noon = new Date(`${dayKey(due)}T12:00:00`);
  const day = Number.isNaN(noon.getTime()) ? String(due) : formatDay(noon);
  if (bucket === 'overdue') return `Overdue, due ${day}`;
  if (bucket === 'today') return 'Due today';
  return `Due ${day}`;
}

export function toLocalInput(seconds: number | null | undefined): string {
  if (!seconds) return '';
  const date = new Date(seconds * 1000);
  const time = `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
  return `${localKey(date)}T${time}`;
}

export function fromLocalInput(value: string): number | undefined {
  const ms = new Date(value).getTime();
  return Number.isNaN(ms) ? undefined : Math.round(ms / 1000);
}

export const linkWords: Record<string, string> = {
  thread: 'Chat',
  project: 'Space',
  person: 'Person',
  record: 'Knowledge',
  message: 'Mail',
  proposal: 'Proposal',
};

export function linkText(link: TaskLink): string {
  const word = linkWords[link.kind] ?? link.kind;
  return `${word}: ${link.title || link.id}`;
}

export function grouped<T extends { due?: string | null }>(items: readonly T[]): { bucket: Bucket; items: T[] }[] {
  const today = todayKey();
  return buckets
    .map((bucket) => ({ bucket, items: items.filter((item) => bucketOf(item.due, today) === bucket) }))
    .filter((group) => group.items.length > 0);
}

export function linkHref(link: TaskLink): string | undefined {
  if (link.kind === 'thread') return p('/chat/:id', { params: { id: link.id } });
  if (link.kind === 'proposal') return inboxPath('proposal', link.id);
  return undefined;
}

export function isModuleOff(error: unknown): boolean {
  return error instanceof ApiError && error.code === 'module_disabled';
}

export const priorities = [
  { value: 'high', label: 'High' },
  { value: 'normal', label: 'Normal' },
  { value: 'low', label: 'Low' },
] as const;

export const linkKinds = [
  { value: '', label: 'Nothing' },
  { value: 'thread', label: 'Chat' },
  { value: 'project', label: 'Space' },
  { value: 'person', label: 'Person' },
  { value: 'record', label: 'Knowledge' },
  { value: 'message', label: 'Mail' },
] as const;
