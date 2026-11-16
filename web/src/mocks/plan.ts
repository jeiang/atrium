import { delay, HttpResponse, http } from 'msw';
import type {
  CommitmentItem,
  CommitmentItemStatus,
  QueueItem,
  Task,
  TaskAddRequest,
  TaskLink,
  TaskUpdateRequest,
} from '../contract';
import { API_BASE } from '../contract';

export interface PlanScenario {
  empty?: boolean;
  fail?: boolean;
  conflict?: boolean;
  network?: boolean;
  delayMs?: number;
  many?: number;
}

interface Promise_ {
  id: number;
  owner: 'me' | 'them';
  text: string;
  counterpart: string | null;
  due: string | null;
  status: CommitmentItemStatus;
  message: { id: number; subject: string };
  created_ts: number;
  attention?: boolean;
}

interface Effect {
  id: number;
  undo: () => void;
  undone: boolean;
}

type MockTask = Task & { attention?: boolean };

interface Store {
  tasks: MockTask[];
  promises: Promise_[];
  effects: Effect[];
  nextTask: number;
}

const NOW = 1_795_600_000;
const log = globalThis as typeof globalThis & {
  atriumInboxRequests?: { method: string; path: string; key: string | null }[];
};

function day(offset: number): string {
  const date = new Date();
  date.setDate(date.getDate() + offset);
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
}

function task(id: number, title: string, rest: Partial<MockTask> = {}): MockTask {
  return {
    id,
    ref: `task:${id}`,
    title,
    notes: '',
    status: 'open',
    priority: 'normal',
    source_kind: '',
    source_id: '',
    created_ts: NOW - id * 3600,
    updated_ts: NOW - id * 3600,
    links: [],
    due: null,
    done_ts: null,
    remind_at: null,
    ...rest,
  };
}

function seed(state: PlanScenario): Store {
  const extra = Array.from({ length: state.many ?? 0 }, (_, i) =>
    task(100 + i, `Sort photo album ${i + 1}`, { due: day(20 + i) }),
  );
  return {
    nextTask: 200,
    effects: [],
    tasks: state.empty
      ? []
      : [
          task(31, 'Renew the library card', {
            due: day(-1),
            priority: 'high',
            notes: 'The branch on Elm Street closes at five.',
            source_kind: 'thread',
            source_id: '4',
            links: [{ kind: 'thread', id: '4', title: 'Weekend errands' }],
            attention: true,
          }),
          task(32, 'Book the bike service', {
            due: day(3),
            source_kind: 'project',
            source_id: '2',
            links: [{ kind: 'project', id: '2', title: 'Garden club' }],
          }),
          task(33, 'Return the borrowed ladder', {
            source_kind: 'person',
            source_id: 'sam@example.test',
            links: [{ kind: 'person', id: 'sam@example.test', title: 'sam@example.test' }],
          }),
          task(34, 'Order seeds for spring', { status: 'done', done_ts: NOW - 7200 }),
          task(35, 'Water the seedlings', { status: 'dismissed' }),
          ...extra,
        ],
    promises: state.empty
      ? []
      : [
          {
            id: 8,
            owner: 'me',
            text: 'Send Sam the bed measurements',
            counterpart: 'Sam Okafor',
            due: day(0),
            status: 'open',
            message: { id: 301, subject: 'Garden plans' },
            created_ts: NOW - 86_400,
            attention: true,
          },
          {
            id: 9,
            owner: 'them',
            text: 'Bring the drill and two spades',
            counterpart: 'Sam Okafor',
            due: day(10),
            status: 'open',
            message: { id: 302, subject: 'Re: Garden plans' },
            created_ts: NOW - 80_000,
          },
          {
            id: 10,
            owner: 'them',
            text: 'Share the seed sheet',
            counterpart: 'Ilse Varga',
            due: null,
            status: 'done',
            message: { id: 303, subject: 'Seed order' },
            created_ts: NOW - 170_000,
          },
        ],
  };
}

let read: string | undefined;
let store: Store = seed({});
let modulesOff = new Set<string>();

function scenario(): PlanScenario {
  try {
    const raw = localStorage.getItem('atrium.mock') ?? '{}';
    const parsed: { plan?: PlanScenario; modules?: Record<string, string> } = JSON.parse(raw);
    if (raw !== read) {
      read = raw;
      store = seed(parsed.plan ?? {});
    }
    modulesOff = new Set(Object.entries(parsed.modules ?? {}).flatMap(([name, s]) => (s === 'off' ? [name] : [])));
    return parsed.plan ?? {};
  } catch {
    return {};
  }
}

function fromTask(t: Task): CommitmentItem {
  return {
    ref: `task:${t.id}`,
    source: 'task',
    id: t.id,
    side: 'todo',
    title: t.title,
    notes: t.notes,
    status: t.status,
    owner: 'me',
    created_ts: t.created_ts,
    updated_ts: t.updated_ts,
    links: t.links,
    due: t.due,
    done_ts: t.done_ts,
    priority: t.priority,
    remind_at: t.remind_at,
    actions: t.status === 'open' ? ['done', 'dismiss', 'edit', 'delete'] : ['reopen', 'edit', 'delete'],
  };
}

function fromPromise(c: Promise_): CommitmentItem {
  const links: TaskLink[] = [{ kind: 'message', id: String(c.message.id), title: c.message.subject }];
  return {
    ref: `commitment:${c.id}`,
    source: 'commitment',
    id: c.id,
    side: c.owner === 'them' ? 'waiting' : 'todo',
    title: c.text,
    notes: '',
    status: c.status,
    owner: c.owner,
    counterpart: c.counterpart,
    created_ts: c.created_ts,
    links,
    due: c.due,
    actions: c.status === 'open' ? ['done', 'dismiss'] : ['reopen'],
  };
}

function visibleTasks(): MockTask[] {
  return modulesOff.has('assistant') ? [] : store.tasks;
}

function items(): CommitmentItem[] {
  return [...visibleTasks().map(fromTask), ...(modulesOff.has('mail') ? [] : store.promises.map(fromPromise))].sort(
    (a, b) => (a.due ?? '9999').localeCompare(b.due ?? '9999') || a.created_ts - b.created_ts,
  );
}

export function planAttention(): QueueItem[] {
  scenario();
  const open = (status: string) => status === 'open';
  const dueOf = (due: string | null | undefined) => Math.floor(new Date(`${due}T09:00:00`).getTime() / 1000);
  return [
    ...visibleTasks()
      .filter((t) => t.attention && open(t.status))
      .map(
        (t): QueueItem => ({
          id: `task:${t.id}`,
          kind: 'attention',
          title: t.title,
          summary: 'Task',
          source: { type: 'task', id: String(t.id) },
          arrived_at: t.created_ts,
          due: dueOf(t.due),
          waiting_for: 'due',
          module: 'assistant',
        }),
      ),
    ...store.promises
      .filter((c) => c.attention && open(c.status))
      .map(
        (c): QueueItem => ({
          id: `commitment:${c.id}`,
          kind: 'attention',
          title: c.text,
          summary: `You promised ${c.counterpart ?? 'someone'}`,
          source: { type: 'commitment', id: String(c.id) },
          arrived_at: c.created_ts,
          due: dueOf(c.due),
          waiting_for: 'due',
          module: 'mail',
        }),
      ),
  ];
}

export function planDecideElsewhere(ref: string): void {
  scenario();
  const [kind, raw] = ref.split(':');
  const id = Number(raw);
  const target = kind === 'task' ? store.tasks.find((t) => t.id === id) : store.promises.find((c) => c.id === id);
  if (target) target.status = 'done';
}

function record(request: Request): void {
  log.atriumInboxRequests = [
    ...(log.atriumInboxRequests ?? []),
    { method: request.method, path: new URL(request.url).pathname, key: request.headers.get('Idempotency-Key') },
  ];
}

function refusal(status: number, code: string, message: string): Response {
  return HttpResponse.json({ error: { code, message } }, { status });
}

function page<T>(all: T[], url: URL): { items: T[]; next_cursor?: string } {
  const start = Number(url.searchParams.get('cursor') || 0);
  const limit = Number(url.searchParams.get('limit') || 50);
  const end = start + limit;
  return end < all.length ? { items: all.slice(start, end), next_cursor: String(end) } : { items: all.slice(start) };
}

function counted(list: { status: string }[]): { open: number; done: number; dismissed: number } {
  return {
    open: list.filter((x) => x.status === 'open').length,
    done: list.filter((x) => x.status === 'done').length,
    dismissed: list.filter((x) => x.status === 'dismissed').length,
  };
}

function matches(title: string, url: URL): boolean {
  const q = url.searchParams.get('q')?.toLowerCase();
  return !q || title.toLowerCase().includes(q);
}

function effect(undo: () => void): number[] {
  const id = 900 + store.effects.length;
  store.effects.push({ id, undo, undone: false });
  return [id];
}

const allowed: Record<string, string[]> = { done: ['open'], dismiss: ['open'], reopen: ['done', 'dismissed'] };
const next: Record<string, string> = { done: 'done', dismiss: 'dismissed', reopen: 'open' };

async function guard(request: Request): Promise<Response | undefined> {
  const state = scenario();
  record(request);
  if (state.delayMs) await delay(state.delayMs);
  if (state.conflict) return refusal(409, 'stale', 'It changed since it was read.');
  return undefined;
}

function transition(
  target: { status: string; updated_ts?: number | null; done_ts?: number | null },
  verb: string,
): number[] | Response {
  if (!allowed[verb]?.includes(target.status)) return refusal(409, 'conflict', `It is already ${target.status}.`);
  const before = { ...target };
  target.status = next[verb] as string;
  if ('done_ts' in target) target.done_ts = verb === 'done' ? Math.floor(Date.now() / 1000) : null;
  if ('updated_ts' in target) target.updated_ts = (target.updated_ts ?? 0) + 1;
  return effect(() => Object.assign(target, before));
}

function findTask(id: unknown): MockTask | undefined {
  return visibleTasks().find((t) => t.id === Number(id));
}

function answer(result: number[] | Response, data: () => object): Response {
  if (result instanceof Response) return result;
  if (scenario().network) return HttpResponse.error();
  return HttpResponse.json({ data: data(), effect_ids: result, undoable: true, audit_id: 1 });
}

export const planHandlers = [
  http.get(`*${API_BASE}/commitments`, async ({ request }) => {
    const state = scenario();
    if (state.delayMs) await delay(state.delayMs);
    if (state.fail) return refusal(500, 'internal', 'The plan store did not answer.');
    const url = new URL(request.url);
    const all = items();
    const side = url.searchParams.get('side');
    const status = url.searchParams.get('status');
    const filtered = all.filter(
      (item) => (!side || item.side === side) && (!status || item.status === status) && matches(item.title, url),
    );
    const { items: shown, next_cursor } = page(filtered, url);
    return HttpResponse.json({
      data: {
        items: shown,
        marks: [],
        counts: {
          todo: counted(all.filter((item) => item.side === 'todo')),
          waiting: counted(all.filter((item) => item.side === 'waiting')),
        },
        ...(next_cursor ? { next_cursor } : {}),
      },
      revision: 1,
    });
  }),
  http.get(`*${API_BASE}/commitments/:ref`, ({ params }) => {
    scenario();
    const item = items().find((x) => x.ref === params.ref);
    if (!item) return refusal(404, 'not_found', 'No such commitment.');
    return HttpResponse.json({ data: { item }, revision: 1 });
  }),
  http.post(`*${API_BASE}/commitments/:ref/:verb`, async ({ params, request }) => {
    const refused = await guard(request);
    if (refused) return refused;
    const ref = String(params.ref);
    const [kind, raw] = ref.split(':');
    const target =
      kind === 'task'
        ? findTask(raw)
        : modulesOff.has('mail')
          ? undefined
          : store.promises.find((c) => c.id === Number(raw));
    if (!target) return refusal(404, 'not_found', 'No such commitment.');
    const result = transition(target, String(params.verb));
    return answer(result, () => ({ ref, status: target.status }));
  }),
  http.patch(`*${API_BASE}/commitments/:ref`, async ({ request }) => {
    record(request);
    return refusal(409, 'read_only', 'A commitment from mail accepts only done, dismiss and reopen.');
  }),
  http.get(`*${API_BASE}/tasks`, async ({ request }) => {
    const state = scenario();
    if (state.delayMs) await delay(state.delayMs);
    if (state.fail) return refusal(500, 'internal', 'The task store did not answer.');
    if (modulesOff.has('assistant')) return refusal(409, 'module_disabled', 'The assistant module is disabled.');
    const url = new URL(request.url);
    const status = url.searchParams.get('status');
    const all = store.tasks;
    const filtered = all
      .filter((t) => (!status || t.status === status) && matches(t.title, url))
      .sort((a, b) => (a.due ?? '9999').localeCompare(b.due ?? '9999') || a.id - b.id);
    const { items: shown, next_cursor } = page(filtered, url);
    return HttpResponse.json({
      data: { items: shown, marks: [], counts: counted(all), ...(next_cursor ? { next_cursor } : {}) },
      revision: 1,
    });
  }),
  http.post(`*${API_BASE}/tasks`, async ({ request }) => {
    const refused = await guard(request);
    if (refused) return refused;
    const body = (await request.json()) as TaskAddRequest;
    if (!body.title?.trim()) return refusal(400, 'invalid', 'title is required');
    const id = store.nextTask++;
    const link = body.link ? [{ kind: body.link.kind, id: body.link.id, title: null }] : [];
    const created = task(id, body.title, {
      due: body.due ?? null,
      priority: body.priority ?? 'normal',
      remind_at: body.remind_at ?? null,
      notes: body.notes ?? '',
      source_kind: body.link?.kind ?? '',
      source_id: body.link?.id ?? '',
      links: link,
      created_ts: Math.floor(Date.now() / 1000),
      updated_ts: Math.floor(Date.now() / 1000),
    });
    store.tasks.push(created);
    const effects = effect(() => {
      store.tasks = store.tasks.filter((t) => t.id !== id);
    });
    if (scenario().network) return HttpResponse.error();
    return HttpResponse.json(
      { data: { task_id: id, status: 'open', task: created }, effect_ids: effects, undoable: true },
      { status: 201 },
    );
  }),
  http.get(`*${API_BASE}/tasks/:id`, ({ params }) => {
    scenario();
    const found = findTask(params.id);
    if (!found) return refusal(404, 'not_found', 'No such task.');
    return HttpResponse.json({ data: { task: found }, revision: 1 });
  }),
  http.patch(`*${API_BASE}/tasks/:id`, async ({ params, request }) => {
    const refused = await guard(request);
    if (refused) return refused;
    const found = findTask(params.id);
    if (!found) return refusal(404, 'not_found', 'No such task.');
    const body = (await request.json()) as TaskUpdateRequest;
    if (body.updated_ts !== undefined && body.updated_ts !== found.updated_ts) {
      return refusal(409, 'stale', 'The task changed since it was read.');
    }
    const before = structuredClone({ ...found });
    if (body.title) found.title = body.title;
    if (body.notes) found.notes = body.notes;
    if (body.due) found.due = body.due;
    if (body.priority) found.priority = body.priority;
    if (body.remind_at !== undefined) found.remind_at = body.remind_at;
    if (body.link) {
      found.source_kind = body.link.kind;
      found.source_id = body.link.id;
      found.links = [{ kind: body.link.kind, id: body.link.id, title: null }];
    }
    for (const field of body.clear ?? []) {
      if (field === 'notes') found.notes = '';
      if (field === 'due') found.due = null;
      if (field === 'remind_at') found.remind_at = null;
      if (field === 'link') Object.assign(found, { source_kind: '', source_id: '', links: [] });
    }
    found.updated_ts += 1;
    return answer(
      effect(() => Object.assign(found, before)),
      () => ({ task_id: found.id, status: found.status, task: found }),
    );
  }),
  http.delete(`*${API_BASE}/tasks/:id`, async ({ params, request }) => {
    const refused = await guard(request);
    if (refused) return refused;
    const found = findTask(params.id);
    if (!found) return refusal(404, 'not_found', 'No such task.');
    store.tasks = store.tasks.filter((t) => t.id !== found.id);
    return answer(
      effect(() => store.tasks.push(found)),
      () => ({ task_id: found.id }),
    );
  }),
  http.post(`*${API_BASE}/tasks/:id/:verb`, async ({ params, request }) => {
    const refused = await guard(request);
    if (refused) return refused;
    const found = findTask(params.id);
    if (!found) return refusal(404, 'not_found', 'No such task.');
    const result = transition(found, String(params.verb));
    return answer(result, () => ({ task_id: found.id, status: found.status, task: found }));
  }),
  http.post(`*${API_BASE}/undo/:entry`, ({ params, request }) => {
    scenario();
    record(request);
    const id = Number(String(params.entry).replace(/^write:/, ''));
    const found = store.effects.find((e) => e.id === id);
    if (!found || found.undone) return HttpResponse.json({ undone: false, error: 'Nothing is left to undo.' });
    found.undo();
    found.undone = true;
    return HttpResponse.json({ undone: true });
  }),
];
