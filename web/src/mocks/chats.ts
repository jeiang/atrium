import { delay, HttpResponse, http, passthrough } from 'msw';
import type {
  Held,
  HistoryLine,
  ListedThread,
  ProposalDetail,
  Run,
  RunStreamEvent,
  Step,
  Thread,
  Transcript,
  TranscriptItem,
  Work,
} from '../contract';
import { API_BASE } from '../contract';
import { configuration, models } from './assistant';
import { G3_RUN, G3_THREAD, g3Events } from './g3';
import { mockSpace } from './knowledge';
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
  queued: Transcript['queued'];
  cost: number;
  question?: { run_id: number; text: string } | null;
  held?: Held | null;
  asks?: { proposal_id: number; run_id: number }[];
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
extraRunScripts.push({
  thread: 15,
  run: 109,
  label: 'Three asks',
  parked: true,
  events: numbered([
    start(15, 109, 'Tidy the house notes'),
    { event: 'text', data: { run_id: 109, text: 'Three of these notes are yours, so I am asking first.' } },
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
  add({
    thread: thread(13, 'Fetch the Debian install image', 150),
    items: [
      item(1, 'user', 'Download the Debian netinstall image.', { run_id: 107 }),
      item(3, 'proposal', 'Add the Debian netinstall torrent', { run_id: 107, proposal_id: 502 }),
    ],
    active: null,
    queued: [],
    cost: 0.02,
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
    queued: [{ id: 1, text: 'Tell me the delivery date too.', created_at: BASE - 90 * 60, now: false }],
    held: { run_id: ORDER_RUN, tool_use_id: 'call-1', tool: 'order_supplies', args: '{"boards": 12}' },
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
  add({
    thread: thread(14, 'Sort the shed receipts', 400),
    items: [
      item(1, 'user', 'Sort every shed receipt by supplier.', { run_id: 108 }),
      item(3, 'stop', 'The run stopped after 24 steps, the most one chat run may take.', { run_id: 108 }),
    ],
    active: null,
    queued: [],
    cost: 0.21,
  });
  add({
    thread: thread(15, 'Tidy the house notes', 10),
    items: [item(1, 'user', 'Update the house notes with the new details.', { run_id: 109 })],
    active: runOf(109, 'parked'),
    queued: [],
    cost: 0.03,
    asks: [601, 602, 603].map((id) => ({ proposal_id: id, run_id: 109 })),
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
  [
    502,
    {
      id: 502,
      kind: 'download_add',
      title: 'Add the Debian netinstall torrent',
      status: 'pending',
      actions: ['approve', 'deny'],
      blocked: false,
      undoable: false,
      created_at: BASE - 300,
      link_thread_id: 13,
      payload: {
        kind: 'action',
        proposal_kind: 'download_add',
        module: 'torrents',
        label: 'Add torrent',
        args: [{ label: 'Name', value: 'debian-13-netinst.iso' }],
        preview: null,
        result: null,
        change: { kind: 'tool_call', tool: 'download_add', args: { name: 'debian-13-netinst.iso' } },
      },
    },
  ],
  ...(
    [
      [601, 'Water shutoff', 'Under the stairs.', 'Behind the boiler.'],
      [602, 'Bin day', 'Tuesday.', 'Wednesday.'],
      [603, 'Gate code', '1234.', '4321.'],
    ] as const
  ).map(
    ([id, subject, before, after]): [number, ProposalDetail] => [
      id,
      {
        id,
        kind: 'permission',
        title: `Edit K${id - 500}`,
        status: 'pending',
        actions: [],
        blocked: false,
        undoable: false,
        created_at: BASE - 200,
        link_thread_id: 15,
        payload: {
          kind: 'permission',
          args: [],
          thread_id: 15,
          run_id: 109,
          change: {
            kind: 'guarded_change',
            title: `Edit K${id - 500}`,
            subject,
            rule: `K${id - 500} is operator-owned`,
            fields: [{ name: 'Body', before, after }],
            note: '',
          },
        },
      },
    ],
  ),
]);

let connections = 0;
let sends = 0;
let nextRun = 1000;
let nextThread = 200;
let nextQueued = 2;

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
  const project = value.thread.project_id ? mockSpace(value.thread.project_id) : undefined;
  return {
    id: value.thread.id,
    title: value.thread.title,
    archived: value.thread.archived,
    pinned: value.thread.pinned,
    project_id: project?.id ?? null,
    project_name: project?.name ?? null,
    cost_usd: value.cost,
    created_at: value.thread.created_at,
    updated_at: value.thread.updated_at,
    run_state: value.active?.state ?? 'done',
  };
}

export function chatsRunning(): number {
  return [...threads.values()].filter((value) => value.active?.state === 'running' && value.thread.id !== G3_THREAD)
    .length;
}

function lastOperator(value: MockThread): TranscriptItem | undefined {
  return value.items.findLast((entry) => entry.kind === 'user');
}

function rerun(value: MockThread, from: number, text: string): number {
  const run = nextRun++;
  extraRunScripts.push(replyScript(value.thread.id, run, text));
  value.items = value.items.filter((entry) => entry.seq < from);
  value.active = runOf(run, 'running');
  value.thread.updated_at = BASE;
  return run;
}

const SUMMARY_SPAN = 16;

function summarized(value: MockThread): number {
  const last = value.items.at(-1)?.seq ?? 0;
  return Math.floor(last / SUMMARY_SPAN) * SUMMARY_SPAN;
}

function summaryLine(value: MockThread, from: number, n: number): HistoryLine {
  const to = from + n - 1;
  const opening = value.items.find((entry) => entry.seq >= from && entry.seq <= to && entry.kind === 'user');
  const text = n === 1 ? (value.items.find((entry) => entry.seq === from)?.text ?? '') : (opening?.text ?? '');
  return {
    thread_id: value.thread.id,
    text: `${n === 1 ? 'Message' : `Messages ${from} to ${to}`}: ${text.slice(0, 60)}`,
    from,
    to,
    n,
    state: n === 1 ? 'direct' : 'summary',
    leaf: n === 1,
  };
}

function workOf(value: MockThread): Work {
  const empty: Work = { needs: [], plan: null, sources: [], subagents: [], files: [], drafts: [], code: [] };
  if (value.thread.id !== 11) return empty;
  return {
    ...empty,
    plan: {
      items: [
        { text: 'Look at the roof photos', done: true },
        { text: 'Count the cracked sheets', done: true },
        { text: 'Price two new sheets', done: false },
      ],
      run_id: 105,
      updated_at: BASE - 300,
    },
    sources: [{ kind: 'web', key: 'roof-sheets', title: 'Roof sheet sizes', url: 'https://example.test/roof-sheets' }],
    subagents: [
      {
        thread_id: 12,
        run_id: 106,
        parent_run_id: 105,
        kind: 'subagent',
        role: 'researcher',
        title: 'Check the roof photos',
        effort: 'low',
        status: 'done',
        steps: 3,
        input: 1200,
        output: 300,
        cache_read: 0,
        cache_write: 0,
        cost: 0.004,
      },
    ],
    files: [
      {
        id: 701,
        name: 'roof-photo.jpg',
        media_type: 'image/jpeg',
        bytes: 204_800,
        origin: 'upload',
        created_at: BASE - 900,
        tokens: 1500,
        thread_id: 11,
      },
    ],
    drafts: [{ id: 31, title: 'Ask the builder for a quote' }],
  };
}

function runDetail(value: MockThread, id: number): Run | undefined {
  if (value.active?.id === id) return value.active;
  const entries = value.items.filter((entry) => entry.run_id === id);
  if (entries.length === 0) return undefined;
  const steps = entries.reduce((sum, entry) => sum + (entry.steps?.length ?? 0), 0);
  const stopped = entries.some((entry) => entry.kind === 'assistant' && entry.text.endsWith('Stopped.'));
  return { id, state: stopped ? 'stopped' : 'done', steps, started_at: BASE - 120, ended_at: BASE - 60, error: null };
}

function startReply(value: MockThread, text: string): number {
  const run = nextRun++;
  extraRunScripts.push(replyScript(value.thread.id, run, text));
  value.items.push(item(nextSeq(value), 'user', text, { run_id: run }));
  value.active = runOf(run, 'running');
  value.thread.updated_at = BASE;
  return run;
}

const resolutions = new Map<number, { outcome: string; effective: string; ts: number }>();

function undoEffect(id: number): Response | undefined {
  const proposal = proposals.get(id - 700);
  if (!proposal) return undefined;
  if (!proposal.undoable || proposal.undone_at) return problem(409, 'not_undoable', 'Nothing is left to undo.');
  Object.assign(proposal, { undone_at: Math.floor(Date.now() / 1000), undoable: false, actions: [] });
  publishReview();
  return HttpResponse.json({ undone: true });
}

function publishReview(): void {
  const channel = new BroadcastChannel('atrium-mock-events');
  channel.postMessage({ topic: 'review' });
  channel.close();
}

function ledger(proposal: ProposalDetail): object {
  const resolution = resolutions.get(proposal.id);
  return {
    id: 700 + proposal.id,
    proposal_id: proposal.id,
    direction: 'forward',
    state: proposal.blocked ? 'unknown' : (resolution?.effective ?? 'done'),
    blocked: proposal.blocked,
    undo_blocked: false,
    undoable: proposal.undoable,
    undone_at: proposal.undone_at ?? null,
    resolution: resolution && {
      id: 800 + proposal.id,
      outcome: resolution.outcome,
      effective_outcome: resolution.effective,
      actor: 'operator',
      source: 'web',
      prior_state: 'unknown',
      prior_error: '',
      ts: resolution.ts,
    },
  };
}

function resolve(id: number, outcome: string | undefined): Response | undefined {
  const proposal = proposals.get(id);
  if (!proposal) return undefined;
  if (!proposal.blocked) return problem(409, 'not_resolved', `proposal ${id} has no blocked write`);
  const effective = { applied: 'done', 'not-applied': 'failed' }[outcome ?? ''];
  if (!effective) return problem(422, 'invalid', 'outcome must be applied, not-applied or partial');
  resolutions.set(id, { outcome: outcome ?? '', effective, ts: Math.floor(Date.now() / 1000) });
  proposal.blocked = false;
  return HttpResponse.json({ approval: { id, status: proposal.status } });
}

function decide(id: number, status: string, reply: string): Response | undefined {
  const proposal = proposals.get(id);
  if (!proposal) return undefined;
  if (proposal.status !== 'pending') return problem(409, 'conflict', 'This ask was already decided.');
  if (status === 'approved' && scenario().approveUnknown) {
    Object.assign(proposal, { status: 'failed', blocked: true, decided_at: BASE });
    return problem(409, 'effect_unknown', 'The mail server did not confirm the send.');
  }
  const applied = status === 'approved';
  const undoable = applied && proposal.kind === 'download_add';
  Object.assign(proposal, {
    status: applied ? 'applied' : status,
    decided_at: BASE,
    applied_at: applied ? BASE : null,
    undoable,
    actions: undoable ? ['undo'] : [],
  });
  const value = proposal.link_thread_id ? threads.get(proposal.link_thread_id) : undefined;
  const open = value?.asks?.some((ask) => proposals.get(ask.proposal_id)?.status === 'pending') ?? false;
  if (value?.active && !open) {
    value.items.push(item(nextSeq(value), 'assistant', reply, { run_id: value.active.id }));
    value.active = null;
    value.asks = [];
  }
  publishReview();
  return HttpResponse.json({
    approval: {
      id,
      kind: proposal.kind,
      kind_label: 'Send email',
      module: 'mail',
      status: proposal.status,
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
    outcome: applied ? 'applied' : 'dismissed',
  });
}

export const chatHandlers = [
  http.get(`*${API_BASE}/threads`, ({ request }) => {
    const state = scenario();
    if (state.listError) return problem(503, 'unavailable', 'The assistant is not answering.');
    const url = new URL(request.url);
    const view = url.searchParams.get('view') ?? '';
    if (view !== '' && view !== 'archived') return problem(422, 'invalid', 'unknown view');
    const limit = Number(url.searchParams.get('limit') ?? '50');
    const offset = Number(url.searchParams.get('cursor') ?? '0');
    const all = state.empty
      ? []
      : [...threads.values()]
          .filter((value) => value.thread.archived === (view === 'archived'))
          .sort(
            (a, b) =>
              Number(b.thread.pinned) - Number(a.thread.pinned) ||
              b.thread.updated_at - a.thread.updated_at ||
              b.thread.id - a.thread.id,
          );
    const page = all.slice(offset, offset + limit);
    const next = offset + limit < all.length ? String(offset + limit) : null;
    return HttpResponse.json({ items: page.map(listed), next_cursor: next });
  }),
  http.put(`*${API_BASE}/threads/:thread/pin`, async ({ request, params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'unknown thread');
    const body = (await request.json()) as { pinned?: boolean };
    value.thread.pinned = body.pinned ?? false;
    return HttpResponse.json({ thread_id: value.thread.id });
  }),
  http.put(`*${API_BASE}/threads/:thread/archive`, async ({ request, params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'unknown thread');
    const body = (await request.json()) as { archived: boolean };
    value.thread.archived = body.archived;
    return HttpResponse.json({ thread_id: value.thread.id });
  }),
  http.put(`*${API_BASE}/threads/:thread/project`, async ({ request, params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'unknown chat thread');
    const body = (await request.json()) as { project_id?: number };
    const target = body.project_id ?? 0;
    if (target !== 0) {
      const space = mockSpace(target);
      if (!space) return problem(404, 'not_found', 'unknown space');
      if (space.archived)
        return problem(409, 'conflict', `“${space.name}” is archived. Restore it to move chats into it.`);
    }
    value.thread.project_id = target === 0 ? null : target;
    return HttpResponse.json({ thread_id: value.thread.id });
  }),
  http.delete(`*${API_BASE}/threads/:thread`, ({ params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', `unknown thread ${String(params.thread)}`);
    if (value.active) return problem(409, 'conflict', `thread ${value.thread.id} has a run in progress`);
    threads.delete(value.thread.id);
    return new HttpResponse(null, { status: 204 });
  }),
  http.post(`*${API_BASE}/threads/:thread/regenerate`, ({ params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'unknown thread');
    if (value.active) return problem(409, 'conflict', `thread ${value.thread.id} has a run in progress`);
    const last = lastOperator(value);
    if (!last) return problem(409, 'conflict', 'no operator message to answer again');
    const run = rerun(value, last.seq + 1, last.text);
    last.run_id = run;
    return HttpResponse.json({ thread_id: value.thread.id, run_id: run }, { status: 202 });
  }),
  http.patch(`*${API_BASE}/threads/:thread/messages/latest`, async ({ request, params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'unknown thread');
    const { text } = (await request.json()) as { text?: string };
    if (!text?.trim()) return problem(422, 'invalid', 'Write a message first.');
    if (value.active) return problem(409, 'conflict', `thread ${value.thread.id} has a run in progress`);
    const last = lastOperator(value);
    if (!last) return problem(409, 'conflict', 'no operator message to edit');
    const run = rerun(value, last.seq, text);
    value.items.push(item(last.seq, 'user', text, { run_id: run }));
    return HttpResponse.json({ run_id: run }, { status: 202 });
  }),
  http.post(`*${API_BASE}/threads/:thread/branch`, async ({ request, params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', `unknown thread ${String(params.thread)}`);
    const { seq } = (await request.json()) as { seq?: number };
    if (!value.items.some((entry) => entry.seq === seq))
      return problem(409, 'conflict', `no message ${seq ?? 0} to branch from`);
    const id = nextThread++;
    const copied: MockThread = {
      thread: { ...value.thread, id, pinned: false, archived: false, branched_from_thread_id: value.thread.id },
      items: value.items.filter((entry) => entry.seq <= (seq ?? 0)).map((entry) => ({ ...entry })),
      active: null,
      queued: [],
      cost: 0,
    };
    copied.thread.created_at = BASE;
    copied.thread.updated_at = BASE;
    threads.set(id, copied);
    return HttpResponse.json({ thread_id: id }, { status: 201 });
  }),
  http.get(`*${API_BASE}/threads/:thread/history`, ({ request, params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'unknown thread');
    const url = new URL(request.url);
    const n = Number(url.searchParams.get('n') ?? '0');
    const start = Number(url.searchParams.get('start') ?? '0');
    const covered = summarized(value);
    if (n === 0) {
      const lines: HistoryLine[] = [];
      for (let from = 1; from <= covered; from += SUMMARY_SPAN) lines.push(summaryLine(value, from, SUMMARY_SPAN));
      return HttpResponse.json({ lines, leaf: null });
    }
    const valid = n & (n - 1) ? false : start >= 1 && (start - 1) % n === 0 && start + n - 1 <= covered;
    if (!valid || n > SUMMARY_SPAN) return problem(404, 'not_found', 'unknown range');
    if (n === 1) {
      const leaf = value.items.find((entry) => entry.seq === start);
      if (!leaf) return problem(404, 'not_found', 'unknown range');
      return HttpResponse.json({ lines: [], leaf: leaf.text });
    }
    const half = n / 2;
    return HttpResponse.json({
      lines: [summaryLine(value, start, half), summaryLine(value, start + half, half)],
      leaf: null,
    });
  }),
  http.get(`*${API_BASE}/threads/:thread/work`, ({ params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'unknown thread');
    return HttpResponse.json(workOf(value));
  }),
  http.get(`*${API_BASE}/threads/:thread/runs/:run`, ({ params }) => {
    const value = threads.get(Number(params.thread));
    const run = value ? runDetail(value, Number(params.run)) : undefined;
    if (!run) return problem(404, 'not_found', 'unknown run');
    return HttpResponse.json(run);
  }),
  http.post(`*${API_BASE}/threads`, async ({ request }) => {
    const body = (await request.json()) as {
      text?: string;
      model?: string;
      effort?: string;
      context_limit?: number;
      thinking?: string;
    };
    const text = body.text?.trim() ?? '';
    if (!text) return problem(422, 'invalid', 'Write a message first.');
    const created: MockThread = {
      thread: {
        ...thread(nextThread++, text.slice(0, 48), 0),
        model: body.model ?? 'claude-sonnet-5-5',
        effort: body.effort ?? 'high',
        context_limit: body.context_limit ?? 200_000,
        thinking: body.thinking ?? 'adaptive',
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
      held: value.held ?? null,
      asks: value.asks ?? [],
    };
    return HttpResponse.json(body);
  }),
  http.post(`*${API_BASE}/threads/:thread/messages`, async ({ request, params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'This chat does not exist.');
    if (sends++ < (scenario().failSends ?? 0)) return problem(503, 'unavailable', 'The assistant is not answering.');
    const { text, now } = (await request.json()) as { text: string; now?: boolean };
    if (value.active?.state === 'parked' && now) {
      value.question = null;
      value.active = null;
      return HttpResponse.json({ run_id: startReply(value, text) }, { status: 202 });
    }
    if (value.active) {
      const queued = { id: nextQueued++, text, created_at: BASE, now: now === true };
      value.queued.push(queued);
      return HttpResponse.json({ queued_id: queued.id }, { status: 202 });
    }
    return HttpResponse.json({ run_id: startReply(value, text) }, { status: 202 });
  }),
  http.delete(`*${API_BASE}/threads/:thread/queued/:queued`, ({ params }) => {
    const value = threads.get(Number(params.thread));
    if (!value) return problem(404, 'not_found', 'This chat does not exist.');
    const id = Number(params.queued);
    const found = value.queued.find((entry) => entry.id === id);
    if (!found) {
      if (id < nextQueued) return problem(409, 'conflict', 'The run already took this message.');
      return problem(404, 'not_found', 'No such queued message in this thread.');
    }
    value.queued = value.queued.filter((entry) => entry.id !== id);
    return HttpResponse.json({ id: found.id, text: found.text });
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
      value.held = null;
      for (const entry of value.queued)
        value.items.push(item(nextSeq(value), 'user', entry.text, { run_id: ORDER_RUN }));
      value.queued = [];
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
    const body = (await request.json()) as { model?: string; effort?: string; context_limit?: number };
    if (body.model) value.thread.model = body.model;
    if (body.effort) value.thread.effort = body.effort;
    if (body.context_limit) value.thread.context_limit = body.context_limit;
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
  http.post(`*${API_BASE}/approvals/:id/resolve`, async ({ params, request }) => {
    const { outcome } = (await request.clone().json()) as { outcome?: string };
    return resolve(Number(params.id), outcome);
  }),
  http.get(`*${API_BASE}/effects`, ({ request }) => {
    const proposal = proposals.get(Number(new URL(request.url).searchParams.get('proposal_id')));
    return proposal ? HttpResponse.json({ items: [ledger(proposal)] }) : undefined;
  }),
  http.post(`*${API_BASE}/effects/:id/undo`, ({ params }) => undoEffect(Number(params.id))),
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
