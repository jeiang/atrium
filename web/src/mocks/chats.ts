import { delay, HttpResponse, http, passthrough } from 'msw';
import type {
  ListedThread,
  ProposalDetail,
  Run,
  RunStreamEvent,
  Step,
  Thread,
  Transcript,
  TranscriptItem,
} from '../contract';
import { API_BASE } from '../contract';
import { configuration, models } from './assistant';
import { G3_RUN, G3_THREAD, g3Events } from './g3';
import { extraRunScripts, type RunScript } from './runs';
import { findRunScript, simulateRun } from './simulator';

export interface ChatScenario {
  speed?: number;
  faults?: string[];
  transcriptDelay?: number;
  failSends?: number;
  empty?: boolean;
  listError?: boolean;
  liveStream?: boolean;
  resumeFault?: boolean;
  approveUnknown?: boolean;
}

interface MockThread {
  thread: Thread;
  items: TranscriptItem[];
  active: Run | null;
  queued: { id: number; text: string; created_at: number }[];
  cost: number;
  question?: { run_id: number; text: string } | null;
}

const BASE = Date.UTC(2026, 10, 16, 9, 0) / 1000;
const SCENARIO_KEY = 'atrium.mock';
const decoder = new TextDecoder();

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
    data: {
      run_id: run,
      thread_id: thread,
      thread_kind: 'chat',
      role: null,
      title,
      effort: 'high',
      parent_run_id: null,
    },
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

extraRunScripts.push({ thread: G3_THREAD, run: G3_RUN, label: 'Long run', parked: false, events: g3Events() });
extraRunScripts.push({
  thread: 10,
  run: 104,
  label: 'Asks',
  parked: true,
  events: numbered([
    start(10, 104, 'Pick the shed paint'),
    { event: 'text', data: { run_id: 104, text: 'Two paints suit cedar: sage green and warm cream.' } },
  ]),
});

const ORDER_THREAD = 12;
const ORDER_RUN = 106;
const orderEvents = numbered([
  start(ORDER_THREAD, ORDER_RUN, 'Order the cedar boards'),
  { event: 'text', data: { run_id: ORDER_RUN, text: 'I will order twelve cedar boards from Hillfield Timber.' } },
  {
    event: 'tool_start',
    data: { run_id: ORDER_RUN, id: 'call-1', name: 'order_supplies', args: JSON.stringify({ boards: 12 }) },
  },
  { event: 'tool_done', data: { run_id: ORDER_RUN, id: 'call-1', ok: false, preview: 'The connection dropped' } },
  {
    event: 'tool_start',
    data: { run_id: ORDER_RUN, id: 'call-2', name: 'check_order', args: JSON.stringify({ supplier: 'Hillfield' }) },
  },
  { event: 'tool_done', data: { run_id: ORDER_RUN, id: 'call-2', ok: true, preview: 'Order 4471 for 12 boards' } },
  {
    event: 'text',
    data: { run_id: ORDER_RUN, text: 'The order for twelve boards went through, so I did not place it again.' },
  },
  { event: 'run_done', data: { run_id: ORDER_RUN, state: 'done', error: null } },
  { event: 'complete', data: { run_id: ORDER_RUN, state: 'done', thread_id: ORDER_THREAD } },
]);
const orderScript: RunScript = {
  thread: ORDER_THREAD,
  run: ORDER_RUN,
  label: 'Interrupted',
  parked: true,
  interrupted: true,
  events: orderEvents.slice(0, 3),
};
extraRunScripts.push(orderScript);

function thread(id: number, title: string, minutesAgo: number): Thread {
  const at = BASE - minutesAgo * 60;
  return {
    id,
    title,
    kind: 'chat',
    model: 'claude-sonnet-5-5',
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
      item(
        turn * 2,
        'assistant',
        `Answer ${turn}: the manual lists **175/65 R14** for winter use.\n\nCheck the sidewall first.`,
      ),
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
    thread: thread(10, 'Pick the shed paint', 45),
    items: [item(1, 'user', 'Choose a paint for the shed.', { run_id: 104 })],
    active: runOf(104, 'parked'),
    queued: [],
    cost: 0.02,
    question: { run_id: 104, text: 'Which do you prefer, **sage green** or **warm cream**?' },
  });
  add({
    thread: thread(G3_THREAD, 'Compare the winter tyre quotes', 5),
    items: [item(1, 'user', 'Compare every winter tyre quote I saved.', { run_id: G3_RUN })],
    active: runOf(G3_RUN, 'running'),
    queued: [],
    cost: 0.5,
  });
  add({
    thread: thread(ORDER_THREAD, 'Order the cedar boards', 100),
    items: [item(1, 'user', 'Order twelve cedar boards for the shed.', { run_id: ORDER_RUN })],
    active: runOf(ORDER_RUN, 'interrupted'),
    queued: [],
    cost: 0.05,
  });
  add({
    thread: thread(11, 'Check the shed roof', 20),
    items: [
      item(1, 'user', 'Does the shed roof need new sheets?', { run_id: 105 }),
      item(2, 'activity', '', { run_id: 105, steps: [thinking('Two sheets are cracked in the photos.')] }),
      item(2, 'assistant', 'Yes: **two** roof sheets need replacing.', { run_id: 105 }),
    ],
    active: null,
    queued: [],
    cost: 0.03,
  });
  for (let index = 0; index < 33; index++) {
    add({
      thread: thread(300 + index, `Notes on the allotment, week ${index + 1}`, 600 + index * 240),
      items: [
        item(1, 'user', `Summarise week ${index + 1} at the allotment.`),
        item(2, 'assistant', 'Beans did well.'),
      ],
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

function thinking(text: string): Step {
  return blank('thinking', null, text);
}

function blank(kind: string, id: string | null, text: string): Step {
  return {
    kind,
    id,
    name: '',
    args: '',
    text,
    state: null,
    is_error: false,
    child_thread_id: null,
    error: null,
    results: [],
    result: null,
  };
}

type Block = { kind: 'thinking' | 'text'; text: string } | { kind: 'tool_start'; data: Record<string, unknown> };

function runItems(events: RunStreamEvent[], run: number, first: number): TranscriptItem[] {
  const turns: Block[][] = [[]];
  const done: Record<string, unknown>[] = [];
  let afterResults = false;
  for (const event of events) {
    const data = event.data as Record<string, unknown>;
    if (data.run_id !== run) continue;
    if (event.event === 'tool_done') {
      done.push(data);
      afterResults = true;
      continue;
    }
    if (event.event !== 'thinking' && event.event !== 'text' && event.event !== 'tool_start') continue;
    if (afterResults) turns.push([]);
    afterResults = false;
    const turn = turns.at(-1) ?? [];
    const last = turn.at(-1);
    if (event.event === 'tool_start') turn.push({ kind: 'tool_start', data });
    else if (last && last.kind === event.event) last.text += String(data.text);
    else turn.push({ kind: event.event, text: String(data.text) });
  }
  const items: TranscriptItem[] = [];
  const calls = new Map<string, Step>();
  let activity: TranscriptItem | undefined;
  const step = (seq: number, found: Step): void => {
    if (!activity) {
      activity = item(seq, 'activity', '', { run_id: run, steps: [] });
      items.push(activity);
    }
    activity.steps?.push(found);
  };
  turns.forEach((turn, index) => {
    const seq = first + index * 2;
    const final = !turn.some((block) => block.kind === 'tool_start');
    for (const block of turn) {
      if (block.kind === 'tool_start') {
        const found = blank('tool', String(block.data.id), '');
        found.name = String(block.data.name);
        found.args = String(block.data.args);
        calls.set(String(block.data.id), found);
        step(seq, found);
      } else if (block.kind === 'thinking') {
        if (block.text) step(seq, thinking(block.text));
      } else if (!final) {
        if (block.text.trim()) step(seq, blank('note', null, block.text));
      } else {
        activity = undefined;
        const last = items.at(-1);
        if (last?.kind === 'assistant' && last.seq === seq) last.text += block.text;
        else items.push(item(seq, 'assistant', block.text, { run_id: run }));
      }
    }
  });
  for (const data of done) {
    const found = calls.get(String(data.id));
    if (!found) continue;
    found.result = String(data.preview);
    found.is_error = data.ok === false;
    found.results = (data.results as Step['results'] | undefined) ?? [];
  }
  return items;
}

function finish(threadId: number, run: number, state: string): void {
  const value = threads.get(threadId);
  if (!value || value.active?.id !== run) return;
  value.items.push(...runItems(findRunScript(threadId, run)?.events ?? [], run, nextSeq(value)));
  if (state === 'stopped') {
    const last = value.items.at(-1);
    if (last?.kind === 'assistant' && last.run_id === run) last.text += '\n\nStopped.';
    else value.items.push(item(nextSeq(value), 'assistant', 'Stopped.', { run_id: run }));
  }
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
        if ((state === 'parked' || state === 'interrupted') && value?.active?.id === run) value.active.state = state;
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

function decide(id: number, status: string, reply: string): Response | undefined {
  const proposal = proposals.get(id);
  if (!proposal) return undefined;
  if (proposal.status !== 'pending') return problem(409, 'conflict', 'This ask was already decided.');
  if (status === 'approved' && scenario().approveUnknown) {
    Object.assign(proposal, { status, blocked: true, decided_at: BASE });
    return problem(409, 'effect_unknown', 'The mail server did not confirm the send.');
  }
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
      thread: {
        ...thread(nextThread++, text.slice(0, 48), 0),
        model: body.model ?? 'claude-sonnet-5-5',
        effort: body.effort ?? 'high',
      },
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
      question: value.question ?? null,
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
  http.post(`*${API_BASE}/threads/:thread/answer`, async ({ request, params }) => {
    const value = threads.get(Number(params.thread));
    if (!value?.question) return problem(409, 'not_answerable', 'There is no question to answer.');
    const { text } = (await request.json()) as { text: string };
    value.question = null;
    value.active = null;
    return HttpResponse.json({ run_id: startReply(value, text) }, { status: 202 });
  }),
  http.post(`*${API_BASE}/threads/:thread/runs/:run/resume`, ({ params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'This chat does not exist.');
    const active = value.active;
    if (!active || active.id !== Number(params.run) || (active.state !== 'interrupted' && active.state !== 'parked'))
      return problem(409, 'conflict', 'The run cannot be resumed.');
    active.state = 'running';
    if (active.id === ORDER_RUN) {
      orderScript.events = orderEvents;
      orderScript.parked = false;
      orderScript.interrupted = false;
    }
    if (scenario().resumeFault) return HttpResponse.error();
    return HttpResponse.json({ thread_id: value.thread.id }, { status: 202 });
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
  http.get(`*${API_BASE}/composer-options`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const model = query.get('model') ?? '';
    if (!models.some((entry) => entry.id === model))
      return problem(422, 'invalid', 'The thread options are not valid.');
    const body = configuration();
    return HttpResponse.json({
      ...body,
      composer: { ...body.composer, model, effort: query.get('effort') || body.composer.effort },
    });
  }),
  http.get(`*${API_BASE}/proposals/:id`, ({ params }) => {
    const proposal = proposals.get(Number(params.id));
    if (!proposal) return undefined;
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
    if (state.liveStream && threadId === G3_THREAD && run === G3_RUN) {
      finish(threadId, run, 'done');
      return passthrough();
    }
    const fault = state.faults?.[connections++];
    url.searchParams.set('speed', String(state.speed ?? 4));
    if (fault) url.searchParams.set('fault', fault);
    if (fault === 'expired') finish(threadId, run, 'done');
    const response = simulateRun(new Request(url, { headers: request.headers, signal: request.signal }), threadId, run);
    if (response.type === 'error') return response;
    if (response.status === 204) {
      finish(threadId, run, 'done');
      return new HttpResponse(null, { status: 204 });
    }
    if (response.status !== 200 || !response.body) {
      return new HttpResponse(await response.text(), {
        status: response.status,
        headers: { 'Content-Type': response.headers.get('Content-Type') ?? 'application/json' },
      });
    }
    const relay = response.body.pipeThrough(watch(threadId, run));
    return new HttpResponse(relay, {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' },
    });
  }),
];
