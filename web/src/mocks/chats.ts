import { delay, HttpResponse, http } from 'msw';
import type {
  ListedThread,
  Model,
  ModelConfiguration,
  ProposalDetail,
  Run,
  RunStreamEvent,
  Step,
  Thread,
  Transcript,
  TranscriptItem,
} from '../contract';
import { API_BASE } from '../contract';
import { extraRunScripts, type RunScript } from './runs';
import { findRunScript } from './simulator';

export interface ChatScenario {
  speed?: number;
  faults?: string[];
  transcriptDelay?: number;
  failSends?: number;
  empty?: boolean;
  listError?: boolean;
}

interface MockThread {
  thread: Thread;
  items: TranscriptItem[];
  active: Run | null;
  queued: { id: number; text: string; created_at: number }[];
  cost: number;
}

const BASE = Date.UTC(2026, 10, 16, 9, 0) / 1000;
const SCENARIO_KEY = 'atrium.mock';
const decoder = new TextDecoder();

export const G3_THREAD = 50;
export const G3_RUN = 900;
export const G3_EVENTS = 2000;

const models: Model[] = [
  model('sonnet-5.5', 'Sonnet 5.5', ['low', 'medium', 'high'], 15),
  model('opus-5', 'Opus 5', ['low', 'medium', 'high', 'max'], 75),
  model('haiku-5', 'Haiku 5', ['low', 'medium'], 5),
];

function model(id: string, name: string, efforts: string[], output: number): Model {
  return {
    id,
    name,
    efforts,
    context_window: 200_000,
    max_output: 64_000,
    thinking: [],
    rates: { input: output / 5, output, cache_read: output / 50, cache_write_5m: output / 4, cache_write_1h: output / 2.5 },
    forced_tools: false,
    run_cap_usd: 5,
  };
}

function scenario(): ChatScenario {
  try {
    const value = JSON.parse(localStorage.getItem(SCENARIO_KEY) ?? '{}') as { chat?: ChatScenario };
    return value.chat ?? {};
  } catch {
    return {};
  }
}

function numbered(events: Omit<RunStreamEvent, 'id'>[]): RunStreamEvent[] {
  let next = 0;
  return events.map((event) =>
    event.event === 'tool_progress' ? (event as RunStreamEvent) : ({ ...event, id: String(++next) } as RunStreamEvent),
  );
}

function start(thread: number, run: number, title: string): Omit<RunStreamEvent, 'id'> {
  return {
    event: 'run_start',
    data: { run_id: run, thread_id: thread, thread_kind: 'chat', role: null, title, effort: 'high', parent_run_id: null },
  };
}

function replyScript(thread: number, run: number, text: string): RunScript {
  return {
    thread,
    run,
    label: 'Reply',
    parked: false,
    events: numbered([
      start(thread, run, text.slice(0, 40)),
      {
        event: 'tool_start',
        data: { run_id: run, id: 'call-1', name: 'search_notes', args: JSON.stringify({ query: text.slice(0, 40) }) },
      },
      { event: 'tool_done', data: { run_id: run, id: 'call-1', ok: true, preview: 'Found 2 notes' } },
      { event: 'text', data: { run_id: run, text: 'Here is what your notes say:\n\n' } },
      { event: 'text', data: { run_id: run, text: '- The cedar boards are in stock\n' } },
      { event: 'text', data: { run_id: run, text: '- Delivery takes **three** days\n' } },
      { event: 'run_done', data: { run_id: run, state: 'done', error: null } },
      { event: 'complete', data: { run_id: run, state: 'done', thread_id: thread } },
    ]),
  };
}

function g3Script(): RunScript {
  const events: Omit<RunStreamEvent, 'id'>[] = [start(G3_THREAD, G3_RUN, 'Compare the winter tyre quotes')];
  let call = 0;
  while (events.length < G3_EVENTS - 2) {
    const index = events.length;
    if (index % 50 === 1) {
      call++;
      events.push({
        event: 'tool_start',
        data: { run_id: G3_RUN, id: `call-${call}`, name: 'search_web', args: `{"query":"winter tyre quote ${call}"}` },
      });
    } else if (index % 50 === 6) {
      events.push({
        event: 'tool_done',
        data: { run_id: G3_RUN, id: `call-${call}`, ok: true, preview: `Read quote ${call}` },
      });
    } else if (index % 25 === 0) {
      events.push({ event: 'text', data: { run_id: G3_RUN, text: `\n\n## Garage ${index / 25}\n\n` } });
    } else if (index % 7 === 0) {
      events.push({ event: 'text', data: { run_id: G3_RUN, text: `\n- set ${index} costs **${index % 97} units** ` } });
    } else {
      events.push({ event: 'text', data: { run_id: G3_RUN, text: `word${index} ` } });
    }
  }
  events.push({ event: 'run_done', data: { run_id: G3_RUN, state: 'done', error: null } });
  events.push({ event: 'complete', data: { run_id: G3_RUN, state: 'done', thread_id: G3_THREAD } });
  return { thread: G3_THREAD, run: G3_RUN, label: 'Long run', parked: false, events: numbered(events) };
}

extraRunScripts.push(g3Script());

function thread(id: number, title: string, minutesAgo: number): Thread {
  const at = BASE - minutesAgo * 60;
  return {
    id,
    title,
    kind: 'chat',
    model: 'sonnet-5.5',
    effort: 'high',
    thinking: 'adaptive',
    context_limit: 200_000,
    archived: false,
    pinned: false,
    web_search: true,
    created_at: at - 3600,
    updated_at: at,
  };
}

function item(seq: number, kind: string, text: string, extra: Partial<TranscriptItem> = {}): TranscriptItem {
  return { seq, kind, text, created_at: BASE - 600 + seq * 20, files: null, steps: null, citations: null, ...extra };
}

function runOf(id: number, state: string): Run {
  return { id, state, steps: 0, started_at: BASE - 120 };
}

function history(count: number): TranscriptItem[] {
  const out: TranscriptItem[] = [];
  for (let turn = 1; turn <= count; turn++) {
    out.push(item(turn * 2 - 1, 'user', `Question ${turn}: which tyre size fits the small car?`));
    out.push(
      item(turn * 2, 'assistant', `Answer ${turn}: the manual lists **175/65 R14** for winter use.\n\nCheck the sidewall first.`),
    );
  }
  return out;
}

function seed(): Map<number, MockThread> {
  const threads = new Map<number, MockThread>();
  const add = (value: MockThread): void => {
    threads.set(value.thread.id, value);
  };
  add({
    thread: thread(7, 'Plan the garden shed order', 2),
    items: [item(1, 'user', 'What do twelve cedar boards cost around Hillfield?', { run_id: 101 })],
    active: runOf(101, 'running'),
    queued: [],
    cost: 0.12,
  });
  add({
    thread: thread(8, 'Send the book club reminder', 30),
    items: [
      item(1, 'user', 'Remind the book club about Thursday.', { run_id: 103 }),
      item(3, 'proposal', 'Send the book club reminder to 6 people', { run_id: 103, proposal_id: 501 }),
    ],
    active: runOf(103, 'parked'),
    queued: [],
    cost: 0.04,
  });
  add({ thread: thread(9, 'Winter tyre sizes', 90), items: history(40), active: null, queued: [], cost: 0.31 });
  add({
    thread: thread(G3_THREAD, 'Compare the winter tyre quotes', 5),
    items: [item(1, 'user', 'Compare every winter tyre quote I saved.', { run_id: G3_RUN })],
    active: runOf(G3_RUN, 'running'),
    queued: [],
    cost: 0.5,
  });
  for (let index = 0; index < 34; index++) {
    add({
      thread: thread(300 + index, `Notes on the allotment, week ${index + 1}`, 600 + index * 240),
      items: [item(1, 'user', `Summarise week ${index + 1} at the allotment.`), item(2, 'assistant', 'Beans did well.')],
      active: null,
      queued: [],
      cost: 0.01,
    });
  }
  return threads;
}

const threads = seed();
const proposals = new Map<number, ProposalDetail>([
  [
    501,
    {
      id: 501,
      kind: 'tool_call',
      title: 'Send the book club reminder to 6 people',
      status: 'pending',
      actions: ['approve', 'deny'],
      blocked: false,
      undoable: false,
      created_at: BASE - 500,
      link_thread_id: 8,
      payload: {
        kind: 'action',
        proposal_kind: 'tool_call',
        module: 'mail',
        label: 'Send email',
        args: [
          { label: 'To', value: 'book-club@example.test' },
          { label: 'Subject', value: 'Book club on Thursday' },
        ],
        preview: null,
        result: null,
        change: {
          kind: 'tool_call',
          tool: 'mail.send',
          args: { to: 'book-club@example.test', subject: 'Book club on Thursday' },
        },
      },
    },
  ],
]);

let connections = 0;
let sends = 0;
let nextRun = 1000;
let nextThread = 200;
let nextQueued = 1;

function nextSeq(value: MockThread): number {
  return (value.items.at(-1)?.seq ?? 0) + 1;
}

function finish(threadId: number, run: number, state: string): void {
  const value = threads.get(threadId);
  if (!value || value.active?.id !== run) return;
  const script = findRunScript(threadId, run);
  const steps: Step[] = [];
  let text = '';
  for (const event of script?.events ?? []) {
    const data = event.data as Record<string, unknown>;
    if (data.run_id !== run) continue;
    if (event.event === 'text') text += String(data.text);
    if (event.event === 'tool_start') {
      steps.push({
        kind: 'tool',
        id: String(data.id),
        name: String(data.name),
        args: String(data.args),
        text: '',
        is_error: false,
        child_thread_id: null,
        error: null,
        results: [],
        result: null,
      });
    }
    if (event.event === 'tool_done') {
      const step = steps.find((entry) => entry.id === data.id);
      if (step) {
        step.result = String(data.preview);
        step.is_error = data.ok === false;
        step.results = (data.results as Step['results'] | undefined) ?? [];
      }
    }
  }
  if (steps.length > 0) value.items.push(item(nextSeq(value), 'activity', '', { run_id: run, steps }));
  if (state === 'stopped') text = text ? `${text}\n\nStopped.` : 'Stopped.';
  if (text) value.items.push(item(nextSeq(value), 'assistant', text, { run_id: run }));
  value.active = null;
  value.thread.updated_at = BASE;
}

function watch(threadId: number, run: number): TransformStream<Uint8Array, Uint8Array> {
  let buffer = '';
  return new TransformStream({
    transform(chunk, controller) {
      buffer += decoder.decode(chunk, { stream: true });
      const match = /event: done\ndata: (\{[^\n]*\})/.exec(buffer);
      if (match?.[1]) {
        const { state } = JSON.parse(match[1]) as { state: string };
        const value = threads.get(threadId);
        if (state === 'parked' && value?.active?.id === run) value.active.state = 'parked';
        else finish(threadId, run, state);
        buffer = '';
      } else if (buffer.length > 4096) {
        buffer = buffer.slice(-1024);
      }
      controller.enqueue(chunk);
    },
  });
}

function problem(status: number, code: string, message: string): Response {
  return HttpResponse.json({ error: { code, message } }, { status });
}

function listed(value: MockThread): ListedThread {
  return {
    id: value.thread.id,
    title: value.thread.title,
    archived: false,
    pinned: false,
    cost_usd: value.cost,
    created_at: value.thread.created_at,
    updated_at: value.thread.updated_at,
    run_state: value.active?.state ?? 'done',
  };
}

function startReply(value: MockThread, text: string): number {
  const run = nextRun++;
  extraRunScripts.push(replyScript(value.thread.id, run, text));
  value.items.push(item(nextSeq(value), 'user', text, { run_id: run }));
  value.active = runOf(run, 'running');
  value.thread.updated_at = BASE;
  return run;
}

function configuration(): ModelConfiguration {
  const defaults = { model: 'sonnet-5.5', effort: 'high', context_limit: 200_000, thinking: 'adaptive', web_search: true };
  return {
    models,
    defaults,
    efforts: ['low', 'medium', 'high', 'max'],
    context_limits: [200_000],
    composer: {
      ...defaults,
      cliff: false,
      coding: false,
      context: 0,
      context_limits: [200_000],
      effort_fixed: false,
      efforts: ['low', 'medium', 'high'].map((value) => ({ Value: value, Disabled: false })),
      known: true,
      live: 0,
      models,
      thinking_options: [],
    },
  };
}

function decide(id: number, status: string, reply: string): Response {
  const proposal = proposals.get(id);
  if (!proposal) return problem(404, 'not_found', 'This ask does not exist.');
  if (proposal.status !== 'pending') return problem(409, 'conflict', 'This ask was already decided.');
  proposal.status = status;
  proposal.decided_at = BASE;
  const value = proposal.link_thread_id ? threads.get(proposal.link_thread_id) : undefined;
  if (value?.active) {
    value.items.push(item(nextSeq(value), 'assistant', reply, { run_id: value.active.id }));
    value.active = null;
  }
  return HttpResponse.json({
    approval: {
      id,
      kind: proposal.kind,
      kind_label: 'Send email',
      module: 'mail',
      status,
      approval: true,
      args: [],
      blocked: false,
      blocked_effect: false,
      blocked_undo: false,
      created_at: proposal.created_at,
      decided_at: BASE,
      preview: null,
      result: null,
    },
    outcome: status === 'approved' ? 'applied' : 'dismissed',
  });
}

export const chatHandlers = [
  http.get(`*${API_BASE}/threads`, ({ request }) => {
    const state = scenario();
    if (state.listError) return problem(503, 'unavailable', 'The assistant is not answering.');
    const url = new URL(request.url);
    const limit = Number(url.searchParams.get('limit') ?? '50');
    const offset = Number(url.searchParams.get('cursor') ?? '0');
    const all = state.empty
      ? []
      : [...threads.values()].sort((a, b) => b.thread.updated_at - a.thread.updated_at || b.thread.id - a.thread.id);
    const page = all.slice(offset, offset + limit);
    const next = offset + limit < all.length ? String(offset + limit) : null;
    return HttpResponse.json({ items: page.map(listed), next_cursor: next });
  }),
  http.post(`*${API_BASE}/threads`, async ({ request }) => {
    const body = (await request.json()) as { text?: string; model?: string; effort?: string };
    const text = body.text?.trim() ?? '';
    if (!text) return problem(422, 'invalid', 'Write a message first.');
    const created: MockThread = {
      thread: { ...thread(nextThread++, text.slice(0, 48), 0), model: body.model ?? 'sonnet-5.5', effort: body.effort ?? 'high' },
      items: [],
      active: null,
      queued: [],
      cost: 0,
    };
    threads.set(created.thread.id, created);
    const run = startReply(created, text);
    return HttpResponse.json({ thread: created.thread, run_id: run }, { status: 201 });
  }),
  http.get(`*${API_BASE}/threads/:thread`, async ({ request, params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'This chat does not exist.');
    const wait = scenario().transcriptDelay ?? 0;
    if (wait > 0) await delay(wait);
    const url = new URL(request.url);
    const limit = Number(url.searchParams.get('limit') ?? '50');
    const before = Number(url.searchParams.get('cursor') ?? String(Number.MAX_SAFE_INTEGER));
    const older = value.items.filter((entry) => entry.seq < before);
    const page = older.slice(-limit);
    const first = page[0];
    const body: Transcript = {
      thread: value.thread,
      items: page,
      active_run: value.active,
      queued: value.queued,
      cost_usd: value.cost,
      meter: { tokens: 1200, compact_at: 150_000, limit: 200_000, share: 0.006 },
      next_cursor: first && older.length > page.length ? String(first.seq) : null,
      question: null,
    };
    return HttpResponse.json(body);
  }),
  http.post(`*${API_BASE}/threads/:thread/messages`, async ({ request, params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'This chat does not exist.');
    if (sends++ < (scenario().failSends ?? 0)) return problem(503, 'unavailable', 'The assistant is not answering.');
    const { text } = (await request.json()) as { text: string };
    if (value.active) {
      const queued = { id: nextQueued++, text, created_at: BASE };
      value.queued.push(queued);
      return HttpResponse.json({ queued_id: queued.id }, { status: 202 });
    }
    return HttpResponse.json({ run_id: startReply(value, text) }, { status: 202 });
  }),
  http.post(`*${API_BASE}/threads/:thread/stop`, ({ params }) => {
    const value = threads.get(Number(params.thread));
    const run = value?.active?.id;
    if (!value || run === undefined) return problem(409, 'conflict', 'Nothing is running.');
    finish(value.thread.id, run, 'stopped');
    return HttpResponse.json({ stopped: true, run_id: run });
  }),
  http.patch(`*${API_BASE}/threads/:thread/options`, async ({ request, params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'This chat does not exist.');
    const body = (await request.json()) as { model?: string; effort?: string };
    if (body.model) value.thread.model = body.model;
    if (body.effort) value.thread.effort = body.effort;
    return HttpResponse.json({ thread_id: value.thread.id });
  }),
  http.get(`*${API_BASE}/composer-options`, () => HttpResponse.json(configuration())),
  http.get(`*${API_BASE}/proposals/:id`, ({ params }) => {
    const proposal = proposals.get(Number(params.id));
    if (!proposal) return problem(404, 'not_found', 'This ask does not exist.');
    return HttpResponse.json({ revision: `${proposal.id}-${proposal.status}`, data: proposal });
  }),
  http.post(`*${API_BASE}/approvals/:id/approve`, ({ params }) =>
    decide(Number(params.id), 'approved', 'Sent the reminder to 6 people.'),
  ),
  http.post(`*${API_BASE}/approvals/:id/deny`, ({ params }) =>
    decide(Number(params.id), 'dismissed', 'Understood. The reminder was not sent.'),
  ),
  http.get(`*${API_BASE}/threads/:thread/runs/:run/events`, async ({ request, params }) => {
    const url = new URL(request.url);
    if (url.searchParams.has('speed')) return undefined;
    const threadId = Number(params.thread);
    const run = Number(params.run);
    const state = scenario();
    const fault = state.faults?.[connections++];
    url.searchParams.set('speed', String(state.speed ?? 4));
    if (fault) url.searchParams.set('fault', fault);
    if (fault === 'expired') finish(threadId, run, 'done');
    const response = await fetch(url, { headers: request.headers, signal: request.signal });
    if (response.status !== 200 || !response.body) return response;
    return new HttpResponse(response.body.pipeThrough(watch(threadId, run)), {
      status: 200,
      headers: response.headers,
    });
  }),
];
