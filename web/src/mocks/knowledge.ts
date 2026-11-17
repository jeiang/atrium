import { delay, HttpResponse, http } from 'msw';
import type {
  KnowledgeDetail,
  KnowledgeFact,
  KnowledgeHistory,
  KnowledgeLink,
  KnowledgeLinkEnd,
  KnowledgeMap,
  KnowledgeProfile,
  KnowledgeRecord,
  KnowledgeRow,
  KnowledgeSubjectPage,
  KnowledgeSubjectRow,
  LibraryEntry,
  Space,
  SpacePage,
} from '../contract';
import { API_BASE } from '../contract';
import { contentOf, type MockFile, mockFile, pageOf, previewOf, THREAD_TITLES, threadFiles } from './files';

export interface KnowledgeScenario {
  empty?: boolean;
  fail?: boolean;
  slow?: boolean;
  failLinks?: boolean;
  loseLinks?: boolean;
  loseFiles?: boolean;
  losePurge?: boolean;
}

export interface KnowledgeRequest {
  method: string;
  path: string;
  key: string | null;
  ifMatch: string | null;
  body: unknown;
}

interface MockRecord extends KnowledgeRecord {
  kind: string;
  created_at: string;
  facts_about?: number;
  value?: string;
  superseded?: boolean;
}

interface MockLink {
  id: number;
  source: number;
  target: { kind: KnowledgeLinkEnd['kind']; id: string };
  relation: string;
  writer: 'operator' | 'assistant';
  created_at: string;
  removed: boolean;
}

interface MockEffect {
  id: number;
  undo: () => void;
  done: boolean;
}

interface MockSpace {
  space: Space;
  threads: SpacePage['threads'];
  tasks: SpacePage['tasks'];
  files: MockFile[];
}

const MAP_CAP = 50;

const SCENARIO_KEY = 'atrium.mock';
const log = globalThis as typeof globalThis & {
  atriumKnowledgeRequests?: KnowledgeRequest[];
  atriumKnowledge?: { editElsewhere: (id: number) => void };
};

const TASKS: Record<string, { title: string; state: KnowledgeLinkEnd['state'] }> = {
  '7': { title: 'Order raised bed timber', state: 'live' },
  '8': { title: 'Book the tool library drill', state: 'done' },
};

function iso(day: number, hour: number): string {
  return new Date(Date.UTC(2026, 10, day, hour, 15)).toISOString();
}

function record(fields: Partial<MockRecord> & Pick<MockRecord, 'id' | 'title' | 'kind'>): MockRecord {
  return {
    aliases: [],
    body: '',
    confirmed: true,
    marks: [],
    pinned: false,
    private: false,
    retracted: false,
    scope: 'personal',
    source: null,
    updated_at: iso(10, 9),
    writer: 'operator',
    created_at: iso(2, 9),
    ...fields,
  };
}

const ilse = [
  'Ilse runs the **shared garden** on Mill Lane with Sam and two neighbours.',
  '',
  '## How to reach her',
  '',
  '- Calls before ten in the morning work best.',
  '- She reads email in the evening.',
  '',
  '## Current plans',
  '',
  'The new raised beds go behind the shed in December. The compost bins move to the north fence.',
].join('\n');

function seed(): { records: MockRecord[]; links: MockLink[]; history: Record<number, KnowledgeHistory[]> } {
  const records: MockRecord[] = [
    record({
      id: 1,
      title: 'Standing instructions',
      kind: 'wiki',
      pinned: true,
      scope: 'work',
      body: 'Write in plain words. Ask before sending anything on my behalf.',
      source: { origin: 'atrium:instructions' },
      updated_at: iso(1, 8),
    }),
    record({
      id: 21,
      title: 'Ilse de Wit',
      kind: 'wiki',
      aliases: ['Ilse'],
      body: ilse,
      updated_at: iso(14, 10),
      source: { thread_id: 401, seq: 3 },
    }),
    record({
      id: 22,
      title: 'Shared garden project',
      kind: 'wiki',
      body: 'Beds, compost and the seed order for next spring.',
      updated_at: iso(13, 16),
    }),
    record({
      id: 23,
      title: 'Garden weather station',
      kind: 'wiki',
      confirmed: false,
      writer: 'assistant',
      body: 'A small station on the shed roof. It restarts on its own after a power cut.',
      updated_at: iso(12, 11),
      source: { thread_id: 402, run_id: 61, seq: 2 },
    }),
    record({
      id: 24,
      title: 'Water the tomatoes in the morning',
      kind: 'lesson',
      body: 'Evening watering left the leaves wet overnight.',
      updated_at: iso(11, 7),
      scope: 'personal',
      private: true,
    }),
    record({
      id: 31,
      title: 'Birthday',
      kind: 'fact',
      value: '14 March',
      facts_about: 21,
      updated_at: iso(9, 9),
      created_at: iso(3, 9),
    }),
    record({
      id: 32,
      title: 'Prefers calls in the morning',
      kind: 'fact',
      value: 'Before ten',
      facts_about: 21,
      confirmed: false,
      writer: 'assistant',
      updated_at: iso(14, 9),
      created_at: iso(14, 9),
    }),
    record({
      id: 33,
      title: 'Lives on Mill Lane',
      kind: 'fact',
      value: 'Number 4',
      facts_about: 21,
      superseded: true,
      updated_at: iso(8, 9),
      created_at: iso(4, 9),
    }),
    record({
      id: 25,
      title: 'Old compost plan',
      kind: 'wiki',
      retracted: true,
      body: 'Bins by the gate. Replaced by the north fence plan.',
      updated_at: iso(5, 9),
    }),
    record({
      id: 26,
      title: 'Shed roof needs four sheets',
      kind: 'fact',
      confirmed: false,
      writer: 'assistant',
      body: 'Measured during the station repair. Corrugated, two metres each.',
      updated_at: iso(12, 10),
      created_at: iso(12, 10),
      source: { thread_id: 402, run_id: 61, seq: 4 },
    }),
    record({
      id: 27,
      title: 'Seed swap list',
      kind: 'wiki',
      confirmed: false,
      writer: 'import',
      body: 'Beans, chard and two kinds of kale to swap in February.',
      updated_at: iso(6, 9),
      created_at: iso(6, 9),
    }),
    record({
      id: 28,
      title: 'Frost dates',
      kind: 'wiki',
      writer: 'import',
      body: 'Last frost around mid April, first frost late October.',
      updated_at: iso(6, 8),
      created_at: iso(6, 8),
    }),
    record({
      id: 41,
      title: 'Lives in Utrecht',
      kind: 'fact',
      pinned: true,
      body: 'Moved there in 2019 and cycles to the garden.',
      updated_at: iso(1, 5),
      created_at: iso(1, 5),
    }),
    record({
      id: 42,
      title: 'Allergic to wasp stings',
      kind: 'fact',
      pinned: true,
      private: true,
      body: 'Keeps an auto-injector in the shed.',
      updated_at: iso(1, 5),
      created_at: iso(1, 5),
    }),
    record({
      id: 43,
      title: 'Works from home on Fridays',
      kind: 'fact',
      pinned: true,
      confirmed: false,
      writer: 'assistant',
      body: 'Seen in three calendar weeks in a row.',
      updated_at: iso(1, 5),
      created_at: iso(1, 5),
    }),
    record({
      id: 44,
      title: 'Long notes on the allotment rules',
      kind: 'fact',
      pinned: true,
      body: 'Plots are inspected each season and paths stay clear of tools. '.repeat(130).trim(),
      updated_at: iso(1, 5),
      created_at: iso(1, 5),
    }),
  ];
  for (let n = 0; n < 56; n++) {
    records.push(
      record({
        id: 100 + n,
        title: `Seed catalogue note ${n + 1}`,
        kind: 'wiki',
        body: `Notes on seed packet ${n + 1}.`,
        updated_at: iso(1, 6),
        created_at: iso(1, 6),
      }),
    );
  }
  const many: MockLink[] = Array.from({ length: 20 }, (_, n) => ({
    id: 100 + n,
    source: 100 + n,
    target: { kind: 'record', id: '22' },
    relation: 'part_of',
    writer: 'assistant',
    created_at: iso(1, 6),
    removed: false,
  }));
  return {
    records,
    links: [
      {
        id: 71,
        source: 21,
        target: { kind: 'record', id: '22' },
        relation: 'works_with',
        writer: 'operator',
        created_at: iso(6, 9),
        removed: false,
      },
      {
        id: 72,
        source: 21,
        target: { kind: 'task', id: '7' },
        relation: 'about',
        writer: 'assistant',
        created_at: iso(7, 9),
        removed: false,
      },
      {
        id: 73,
        source: 23,
        target: { kind: 'record', id: '21' },
        relation: 'see_also',
        writer: 'assistant',
        created_at: iso(8, 9),
        removed: false,
      },
      {
        id: 74,
        source: 21,
        target: { kind: 'record', id: '24' },
        relation: 'see_also',
        writer: 'operator',
        created_at: iso(9, 9),
        removed: false,
      },
      {
        id: 75,
        source: 21,
        target: { kind: 'record', id: '25' },
        relation: 'see_also',
        writer: 'operator',
        created_at: iso(5, 9),
        removed: false,
      },
      {
        id: 76,
        source: 22,
        target: { kind: 'space', id: '5' },
        relation: 'part_of',
        writer: 'operator',
        created_at: iso(6, 10),
        removed: false,
      },
      ...many,
    ],
    history: {
      1: [
        {
          id: 811,
          at: iso(1, 8),
          tool: 'knowledge.edit',
          by: 'operator',
          previous_body: 'Write in plain words.',
          undone_at: null,
        },
        { id: 810, at: iso(1, 7), tool: 'knowledge.create', by: 'assistant', previous_body: null, undone_at: null },
      ],
      21: [
        {
          id: 801,
          at: iso(14, 10),
          tool: 'knowledge.edit',
          by: 'operator',
          previous_body: 'Ilse runs the shared garden on Mill Lane.',
          undone_at: null,
        },
        { id: 800, at: iso(2, 9), tool: 'knowledge.create', by: 'assistant', previous_body: null, undone_at: null },
      ],
    },
  };
}

function secs(day: number, hour: number): number {
  return Date.UTC(2026, 10, day, hour, 15) / 1000;
}

function space(fields: Partial<Space> & Pick<Space, 'id' | 'name'>): Space {
  return {
    archived: false,
    context_limit: 0,
    created_at: secs(3, 9),
    effort: 'xhigh',
    instructions: '',
    last_activity: null,
    private: false,
    thread_count: 0,
    updated_at: secs(3, 9),
    ...fields,
  };
}

function seedSpaces(): MockSpace[] {
  return [
    {
      space: space({
        id: 5,
        name: 'Shared garden',
        instructions: 'Keep plans **short**. Ask Ilse before changing the bed layout.',
        thread_count: 1,
        last_activity: secs(14, 10),
      }),
      threads: [
        { id: 401, title: 'Planning the raised beds', archived: false, pinned: false, updated_at: secs(14, 10) },
      ],
      tasks: [{ id: 7, title: 'Order raised bed timber', status: 'open', due: '2026-12-01' }],
      files: [
        mockFile(
          {
            id: 602,
            name: 'garden-seeds.csv',
            media_type: 'text/csv',
            project_id: 5,
            created_at: secs(10, 9),
          },
          { text: 'Seed,Sow by,Bed\nBroad beans,November,North\nGarlic,December,South\nSweet peas,March,East\n' },
        ),
        mockFile(
          {
            id: 603,
            name: 'bed-plan.md',
            media_type: 'text/markdown',
            project_id: 5,
            created_at: secs(11, 9),
          },
          { text: '# Bed plan\n\nFour beds, each **1.2 m** wide. Paths stay at 45 cm.\n' },
        ),
      ],
    },
    { space: space({ id: 6, name: 'Studio move', private: true }), threads: [], tasks: [], files: [] },
    {
      space: space({ id: 8, name: 'Old allotment', archived: true, last_activity: secs(2, 9) }),
      threads: [],
      tasks: [],
      files: [
        mockFile(
          { id: 610, name: 'plot-rules.txt', media_type: 'text/plain', project_id: 8, created_at: secs(2, 9) },
          { text: 'Keep paths clear. Bonfires only in October.\n' },
        ),
      ],
    },
  ];
}

let store = seed();
let spaces = seedSpaces();
let effects: MockEffect[] = [];
let nextLink = 200;
let nextEffect = 900;
let nextSpace = 10;
let nextFile = 800;
let nextRecord = 500;
let seededFor = '';

function scenario(): KnowledgeScenario {
  const raw = localStorage.getItem(SCENARIO_KEY) ?? '{}';
  if (raw !== seededFor) {
    seededFor = raw;
    store = seed();
    spaces = seedSpaces();
    effects = [];
  }
  try {
    const parsed: { knowledge?: KnowledgeScenario } = JSON.parse(raw);
    return parsed.knowledge ?? {};
  } catch {
    return {};
  }
}

export function mockSpace(id: number): Space | undefined {
  scenario();
  return spaces.find((item) => item.space.id === id)?.space;
}

function find(id: unknown): MockRecord | undefined {
  return store.records.find((item) => item.id === Number(id));
}

function now(): string {
  return new Date().toISOString();
}

function plain(item: MockRecord): KnowledgeRecord {
  const {
    kind: _kind,
    created_at: _created,
    facts_about: _about,
    value: _value,
    superseded: _superseded,
    ...rest
  } = item;
  return rest;
}

function end(kind: KnowledgeLinkEnd['kind'], id: string): KnowledgeLinkEnd {
  if (kind === 'record') {
    const item = find(id);
    return {
      kind,
      id,
      title: item?.title ?? 'Missing page',
      state: !item ? 'missing' : item.retracted ? 'retracted' : 'live',
      address: `/knowledge/${id}`,
      record_kind: item?.kind,
    };
  }
  if (kind === 'space') {
    const found = spaces.find((item) => item.space.id === Number(id));
    return {
      kind,
      id,
      title: found?.space.name ?? 'Missing space',
      state: !found ? 'missing' : found.space.archived ? 'archived' : 'live',
      address: `/knowledge/spaces/${id}`,
    };
  }
  const task = TASKS[id];
  return {
    kind,
    id,
    title: task?.title ?? 'Missing task',
    state: task?.state ?? 'missing',
    address: `/tasks/${id}`,
  };
}

function neighborhood(center: MockRecord): KnowledgeMap {
  const live = (id: number): MockRecord | undefined => {
    const item = find(id);
    return item && !item.retracted ? item : undefined;
  };
  const edges = store.links
    .filter((link) => !link.removed && link.target.kind === 'record')
    .filter((link) => link.source === center.id || link.target.id === String(center.id))
    .filter((link) => live(link.source) && live(Number(link.target.id)))
    .map((link) => ({ from: link.source, to: Number(link.target.id), kind: link.relation }));
  const ids = [...new Set(edges.flatMap((edge) => [edge.from, edge.to]).filter((id) => id !== center.id))];
  const kept = ids.slice(0, MAP_CAP - 1);
  const node = (item: MockRecord, depth: number) => ({
    id: item.id,
    title: item.title,
    kind: item.kind,
    scope: item.scope,
    private: item.private,
    confirmed: item.confirmed,
    depth,
  });
  return {
    center: center.id,
    nodes: [node(center, 0), ...kept.map((id) => node(find(id) as MockRecord, 1))],
    edges: edges.filter((edge) => kept.includes(edge.from) || kept.includes(edge.to)),
    cap: MAP_CAP,
    truncated: ids.length > kept.length,
  };
}

function spacePage(found: MockSpace): SpacePage {
  const records = store.links
    .filter((link) => !link.removed && link.target.kind === 'space' && link.target.id === String(found.space.id))
    .map((link) => {
      const item = find(link.source);
      return {
        link_id: link.id,
        relation: link.relation,
        id: link.source,
        title: item?.title ?? 'Missing page',
        kind: item?.kind ?? 'wiki',
        state: (!item ? 'missing' : item.retracted ? 'retracted' : 'live') as SpacePage['records'][number]['state'],
        address: `/knowledge/${link.source}`,
        created_at: Date.parse(link.created_at) / 1000,
      };
    });
  return {
    space: found.space,
    sections: [],
    files: found.files.map((file) => ({ ...file.summary, path: file.summary.name, space_id: found.space.id })),
    threads: found.threads,
    tasks: found.tasks,
    records,
    counts: {
      records: records.length,
      threads: found.threads.length,
      tasks: found.tasks.length,
      files: found.files.length,
    },
  };
}

function wire(link: MockLink): KnowledgeLink {
  return {
    id: link.id,
    relation: link.relation,
    writer: link.writer,
    created_at: link.created_at,
    removed: link.removed,
    run_id: null,
    source: end('record', String(link.source)),
    target: end(link.target.kind, link.target.id),
  };
}

function fact(item: MockRecord): KnowledgeFact {
  return {
    id: item.id,
    title: item.title,
    value: item.value ?? '',
    kind: item.kind,
    scope: item.scope,
    private: item.private,
    marks: item.marks,
    writer: item.writer,
    confirmed: item.confirmed,
    superseded: item.superseded ?? false,
    superseded_at: item.superseded ? item.updated_at : null,
    learned_at: item.created_at,
    updated_at: item.updated_at,
    source: item.source,
  };
}

function subjects(): MockRecord[] {
  if (scenario().empty) return [];
  return store.records
    .filter((item) => item.kind !== 'fact' || item.facts_about === undefined)
    .filter((item) => !item.retracted)
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at) || b.id - a.id);
}

function row(item: MockRecord): KnowledgeSubjectRow {
  return {
    id: item.id,
    title: item.title,
    kind: item.kind,
    scope: item.scope,
    private: item.private,
    confirmed: item.confirmed,
    pinned: item.pinned,
    facts: store.records.filter((other) => other.facts_about === item.id && !other.retracted).length,
    updated_at: item.updated_at,
  };
}

function refusal(status: number, code: string, message: string): Response {
  return HttpResponse.json({ error: { code, message } }, { status });
}

async function remember(request: Request): Promise<unknown> {
  let body: unknown = null;
  try {
    body = await request.clone().json();
  } catch {
    body = null;
  }
  log.atriumKnowledgeRequests = [
    ...(log.atriumKnowledgeRequests ?? []),
    {
      method: request.method,
      path: new URL(request.url).pathname,
      key: request.headers.get('Idempotency-Key'),
      ifMatch: request.headers.get('If-Match'),
      body,
    },
  ];
  return body;
}

function effect(undo: () => void): number[] {
  const id = nextEffect++;
  effects.push({ id, undo, done: false });
  return [id];
}

async function gate(): Promise<Response | undefined> {
  const state = scenario();
  if (state.slow) await delay('infinite');
  if (state.fail) return refusal(500, 'internal', 'The knowledge store did not answer.');
  return undefined;
}

function detail(item: MockRecord): KnowledgeDetail {
  const runId = item.source?.run_id;
  const sameRun = runId
    ? store.records.filter((other) => other.source?.run_id === runId && !other.retracted && other.id !== item.id)
    : [];
  return {
    record: plain(item),
    same_run: sameRun.sort((a, b) => a.id - b.id).map(listRow),
    history: store.history[item.id] ?? [],
  };
}

function listRow(item: MockRecord): KnowledgeRow {
  return {
    id: item.id,
    title: item.title,
    scope: item.scope,
    writer: item.writer,
    marks: item.marks,
    confirmed: item.confirmed,
    pinned: item.pinned,
    retracted: item.retracted,
    updated_at: item.updated_at,
  };
}

function listed(filter: { q: string; scope: string; state: string }): MockRecord[] {
  const q = filter.q.trim().toLowerCase();
  return store.records
    .filter(
      (item) =>
        !q ||
        item.title.toLowerCase().includes(q) ||
        item.body.toLowerCase().includes(q) ||
        item.aliases.some((alias) => alias.toLowerCase().includes(q)),
    )
    .filter((item) => !filter.scope || item.scope === filter.scope)
    .filter((item) => {
      if (filter.state === 'unconfirmed') return !item.confirmed && !item.retracted;
      if (filter.state === 'pinned') return item.pinned && !item.retracted;
      if (filter.state === 'retracted') return item.retracted;
      if (filter.state === 'imported') return item.writer === 'import';
      return true;
    })
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at) || b.id - a.id);
}

const PROFILE_HEADER = 'Operator profile (facts confirmed by the operator):';
const PROFILE_BUDGET = 2000;
const SENSITIVE = ['untrusted', 'pii', 'messages'];

function estimate(text: string): number {
  return Math.ceil([...text].length / 4);
}

function profile(): KnowledgeProfile {
  let total = estimate(PROFILE_HEADER);
  const lines: string[] = [];
  const entries = store.records
    .filter((item) => item.pinned && !item.retracted && item.id !== 1)
    .sort((a, b) => a.title.localeCompare(b.title) || a.id - b.id)
    .map((item) => {
      const entry = { id: item.id, title: item.title, body: item.body, reason: '', in_prompt: false, tokens: 0 };
      if (!item.confirmed) entry.reason = 'unconfirmed';
      else if (item.marks.some((mark) => SENSITIVE.includes(mark))) entry.reason = 'marked';
      else if (item.private) entry.reason = 'private';
      else {
        const line = `- ${item.title}: ${item.body}`;
        entry.tokens = estimate(line);
        if (total + entry.tokens > PROFILE_BUDGET) entry.reason = 'over budget';
        else {
          entry.in_prompt = true;
          total += entry.tokens;
          lines.push(line);
        }
      }
      return entry;
    });
  const block = lines.length > 0 ? `${PROFILE_HEADER}\n${lines.join('\n')}` : '';
  const tokens = estimate(block);
  return {
    block,
    tokens,
    budget: PROFILE_BUDGET,
    percent: Math.min(Math.floor((tokens * 100) / PROFILE_BUDGET), 100),
    entries,
  };
}

type Outcome = { refused: string } | { effects: number[] };

function batch(ids: readonly number[], untilRefused: boolean, one: (id: number) => Outcome) {
  const data: { committed: { id: number }[]; refused: { id: number; code: string; message: string }[]; failed: [] } = {
    committed: [],
    refused: [],
    failed: [],
  };
  const effectIds: number[] = [];
  for (const id of ids) {
    const outcome = one(id);
    if ('refused' in outcome) {
      data.refused.push({ id, code: 'refused', message: outcome.refused });
      if (untilRefused) break;
    } else {
      data.committed.push({ id });
      effectIds.push(...outcome.effects);
    }
  }
  return HttpResponse.json({ data, effect_ids: effectIds, undoable: effectIds.length > 0 });
}

function confirmOne(id: number): Outcome {
  const item = find(id);
  if (!item) return { refused: `K${id} is unknown` };
  if (item.retracted) return { refused: `K${id} is retracted` };
  if (item.confirmed) return { effects: [] };
  Object.assign(item, { confirmed: true, updated_at: now() });
  return { effects: effect(() => Object.assign(item, { confirmed: false, updated_at: now() })) };
}

function retractOne(id: number): Outcome {
  const item = find(id);
  if (!item) return { refused: `K${id} is unknown` };
  if (item.retracted) return { effects: [] };
  Object.assign(item, { retracted: true, updated_at: now() });
  return { effects: effect(() => Object.assign(item, { retracted: false, updated_at: now() })) };
}

function invalid(field: string, message: string): Response {
  return HttpResponse.json({ error: { code: 'invalid', message, fields: { [field]: message } } }, { status: 422 });
}

function entryOf(file: MockFile): LibraryEntry {
  const { id, name, media_type, bytes, created_at } = file.summary;
  return { id, name, media_type, bytes, created_at };
}

function spaceFile(params: Record<string, unknown>): MockFile | undefined {
  return spaces
    .find((item) => item.space.id === Number(params.space))
    ?.files.find((file) => file.summary.id === Number(params.file));
}

log.atriumKnowledge = {
  editElsewhere(id: number): void {
    scenario();
    const item = find(id);
    if (!item) return;
    item.body = `${item.body}\n\nSam added a note from the phone.`;
    item.updated_at = now();
  },
};

export const knowledgeHandlers = [
  http.get(`*${API_BASE}/knowledge/spaces`, async ({ request }) => {
    const stop = await gate();
    if (stop) return stop;
    const url = new URL(request.url);
    const limit = Number(url.searchParams.get('limit') ?? '50');
    const offset = Number(url.searchParams.get('cursor') ?? '0');
    const archived = url.searchParams.get('archived') === 'true';
    const all = scenario().empty
      ? []
      : spaces.filter((item) => item.space.archived === archived).map((item) => item.space);
    const next = offset + limit < all.length ? String(offset + limit) : null;
    return HttpResponse.json({ items: all.slice(offset, offset + limit), next_cursor: next });
  }),
  http.get(`*${API_BASE}/files`, async ({ request }) => {
    const stop = await gate();
    if (stop) return stop;
    const entries: LibraryEntry[] = [
      ...threadFiles().map(({ thread, file }) => ({
        ...entryOf(file),
        thread_id: thread,
        thread_title: THREAD_TITLES[thread] ?? '',
      })),
      ...spaces.flatMap((item) =>
        item.files.map((file) => ({ ...entryOf(file), project_id: item.space.id, project_name: item.space.name })),
      ),
    ].sort((a, b) => b.created_at - a.created_at || b.id - a.id);
    return HttpResponse.json(pageOf(scenario().empty ? [] : entries, request));
  }),
  http.get(`*${API_BASE}/knowledge/spaces/:space/files`, async ({ params, request }) => {
    const stop = await gate();
    if (stop) return stop;
    const found = spaces.find((item) => item.space.id === Number(params.space));
    if (!found) return refusal(404, 'not_found', 'unknown space');
    return HttpResponse.json(
      pageOf(
        found.files.map((file) => file.summary),
        request,
      ),
    );
  }),
  http.post(`*${API_BASE}/knowledge/spaces/:space/files`, async ({ params, request }) => {
    await remember(request);
    const found = spaces.find((item) => item.space.id === Number(params.space));
    if (!found) return refusal(404, 'not_found', 'unknown space');
    if (found.space.archived) {
      return refusal(409, 'conflict', `“${found.space.name}” is archived. Restore it to add files.`);
    }
    const form = await request.formData();
    const uploads = form.getAll('upload').filter((value): value is File => value instanceof File);
    if (uploads.length === 0) return refusal(422, 'invalid', 'Choose at least one file.');
    const ids: number[] = [];
    for (const upload of uploads) {
      const media = upload.type || 'application/octet-stream';
      const textual = media.startsWith('text/') || media === 'application/json';
      const id = nextFile++;
      ids.push(id);
      found.files.push(
        mockFile(
          { id, name: upload.name, media_type: media, project_id: found.space.id, created_at: Date.now() / 1000 },
          textual ? { text: await upload.text() } : { data: new Uint8Array(await upload.arrayBuffer()) },
        ),
      );
    }
    if (scenario().loseFiles) return HttpResponse.error();
    return HttpResponse.json({ project_id: found.space.id, file_ids: ids }, { status: 201 });
  }),
  http.delete(`*${API_BASE}/knowledge/spaces/:space/files/:file`, async ({ params, request }) => {
    await remember(request);
    const found = spaces.find((item) => item.space.id === Number(params.space));
    const file = found?.files.find((item) => item.summary.id === Number(params.file));
    if (!found || !file) return refusal(404, 'not_found', 'unknown file');
    if (found.space.archived) {
      return refusal(409, 'conflict', `“${found.space.name}” is archived. Restore it to remove files.`);
    }
    found.files = found.files.filter((item) => item !== file);
    if (scenario().loseFiles) return HttpResponse.error();
    return new HttpResponse(null, { status: 204 });
  }),
  http.get(`*${API_BASE}/knowledge/spaces/:space/files/:file`, async ({ params }) => {
    const stop = await gate();
    if (stop) return stop;
    const file = spaceFile(params);
    return file ? HttpResponse.json(previewOf(file)) : refusal(404, 'not_found', 'unknown file');
  }),
  http.get(`*${API_BASE}/knowledge/spaces/:space/files/:file/content`, ({ params }) => {
    const file = spaceFile(params);
    return file ? contentOf(file) : refusal(404, 'not_found', 'unknown file');
  }),
  http.post(`*${API_BASE}/knowledge/spaces/:space/archive`, async ({ params, request }) => {
    const body = (await remember(request)) as { archived?: boolean } | null;
    const found = spaces.find((item) => item.space.id === Number(params.space));
    if (!found) return refusal(404, 'not_found', 'unknown space');
    found.space = { ...found.space, archived: body?.archived ?? false, updated_at: Date.now() / 1000 };
    if (scenario().loseFiles) return HttpResponse.error();
    return HttpResponse.json({ id: found.space.id });
  }),
  http.post(`*${API_BASE}/knowledge/spaces`, async ({ request }) => {
    const body = (await remember(request)) as { name: string; effort: string; private?: boolean };
    const id = nextSpace++;
    spaces.push({
      space: space({
        id,
        name: body.name,
        effort: body.effort,
        private: body.private ?? false,
        created_at: Date.now() / 1000,
        updated_at: Date.now() / 1000,
      }),
      threads: [],
      tasks: [],
      files: [],
    });
    return HttpResponse.json({ id }, { status: 201 });
  }),
  http.get(`*${API_BASE}/knowledge/spaces/:space`, async ({ params }) => {
    const stop = await gate();
    if (stop) return stop;
    const found = spaces.find((item) => item.space.id === Number(params.space));
    if (!found) return refusal(404, 'not_found', 'No such space.');
    return HttpResponse.json(spacePage(found));
  }),
  http.patch(`*${API_BASE}/knowledge/spaces/:space`, async ({ params, request }) => {
    const body = (await remember(request)) as { name?: string };
    const found = spaces.find((item) => item.space.id === Number(params.space));
    if (!found) return refusal(404, 'not_found', 'No such space.');
    found.space = { ...found.space, name: body.name ?? found.space.name, updated_at: Date.now() / 1000 };
    return HttpResponse.json({ id: found.space.id });
  }),
  http.get(`*${API_BASE}/knowledge/:id/map`, async ({ params }) => {
    const stop = await gate();
    if (stop) return stop;
    const item = find(params.id);
    if (!item) return refusal(404, 'not_found', 'No such knowledge record.');
    return HttpResponse.json({ revision: 1, data: neighborhood(item) });
  }),
  http.get(`*${API_BASE}/knowledge`, async ({ request }) => {
    const stop = await gate();
    if (stop) return stop;
    const url = new URL(request.url);
    const limit = Number(url.searchParams.get('limit') ?? '50');
    const offset = Number(url.searchParams.get('cursor') ?? '0');
    const all = scenario().empty
      ? []
      : listed({
          q: url.searchParams.get('q') ?? '',
          scope: url.searchParams.get('scope') ?? '',
          state: url.searchParams.get('state') ?? 'all',
        });
    const next = offset + limit < all.length ? String(offset + limit) : null;
    return HttpResponse.json({
      revision: 1,
      data: { items: all.slice(offset, offset + limit).map(listRow), next_cursor: next },
    });
  }),
  http.get(`*${API_BASE}/knowledge/profile`, async () => {
    const stop = await gate();
    if (stop) return stop;
    if (scenario().empty) {
      return HttpResponse.json({
        revision: 1,
        data: { block: '', tokens: 0, budget: PROFILE_BUDGET, percent: 0, entries: [] },
      });
    }
    return HttpResponse.json({ revision: 1, data: profile() });
  }),
  http.post(`*${API_BASE}/knowledge`, async ({ request }) => {
    const body = (await remember(request)) as {
      title?: string;
      body?: string;
      scope?: string;
      private?: boolean;
      pinned?: boolean;
    };
    const title = (body.title ?? '').trim();
    const text = (body.body ?? '').trim();
    if (!title) return invalid('title', 'title is required');
    if (!text) return invalid('body', 'body is required');
    if (body.scope !== 'work' && body.scope !== 'personal') return invalid('scope', 'scope must be work or personal');
    const at = now();
    const item = record({
      id: nextRecord++,
      title,
      kind: 'fact',
      body: text,
      scope: body.scope,
      private: body.private ?? false,
      pinned: body.pinned ?? false,
      confirmed: body.pinned ?? false,
      updated_at: at,
      created_at: at,
    });
    store.records.push(item);
    if (item.pinned && profile().entries.find((entry) => entry.id === item.id)?.reason === 'over budget') {
      store.records = store.records.filter((other) => other !== item);
      return refusal(409, 'refused', 'the profile is full: over budget');
    }
    store.history[item.id] = [
      { id: Date.now(), at, tool: 'knowledge.create', by: 'operator', previous_body: null, undone_at: null },
    ];
    const ids = effect(() => Object.assign(item, { retracted: true, updated_at: now() }));
    return HttpResponse.json({ data: { id: item.id, record: plain(item) }, effect_ids: ids }, { status: 201 });
  }),
  http.post(`*${API_BASE}/knowledge/confirm`, async ({ request }) => {
    const body = (await remember(request)) as { ids?: number[] };
    const ids = body.ids ?? [];
    if (ids.length === 0) return invalid('id', 'Select at least one record.');
    return batch(ids, true, confirmOne);
  }),
  http.post(`*${API_BASE}/knowledge/confirm-matching`, async ({ request }) => {
    const body = (await remember(request)) as { state?: string; q?: string; scope?: string };
    if (body.scope && body.scope !== 'work' && body.scope !== 'personal') {
      return invalid('scope', 'scope must be work or personal');
    }
    if (body.state !== 'unconfirmed' && body.state !== 'imported') {
      return invalid('state', 'Choose the unconfirmed or imported filter first.');
    }
    const ids = listed({ q: body.q ?? '', scope: body.scope ?? '', state: body.state })
      .slice(0, 200)
      .filter((item) => !item.confirmed)
      .map((item) => item.id);
    return batch(ids, false, confirmOne);
  }),
  http.post(`*${API_BASE}/knowledge/retract-run`, async ({ request }) => {
    const body = (await remember(request)) as { run_id?: number };
    const ids = store.records
      .filter((item) => body.run_id && item.source?.run_id === body.run_id && !item.retracted)
      .map((item) => item.id)
      .sort((a, b) => a - b);
    return batch(ids, false, retractOne);
  }),
  http.delete(`*${API_BASE}/knowledge/:id`, async ({ params, request }) => {
    await remember(request);
    const id = Number(params.id);
    if (!find(id)) return refusal(409, 'refused', `K${id} is unknown`);
    if (id === 1) {
      return refusal(
        409,
        'refused',
        `K${id} holds the standing instructions and cannot be purged; edit or retract it instead`,
      );
    }
    store.records = store.records.filter((item) => item.id !== id);
    store.links = store.links.filter(
      (link) => link.source !== id && !(link.target.kind === 'record' && link.target.id === String(id)),
    );
    store.history[id] = [];
    if (scenario().losePurge) return HttpResponse.error();
    return new HttpResponse(null, { status: 204 });
  }),
  http.get(`*${API_BASE}/knowledge/subjects`, async ({ request }) => {
    const stop = await gate();
    if (stop) return stop;
    const url = new URL(request.url);
    const q = (url.searchParams.get('q') ?? '').toLowerCase();
    const kind = url.searchParams.get('kind');
    const scope = url.searchParams.get('scope');
    const limit = Number(url.searchParams.get('limit') ?? '50');
    const offset = Number(url.searchParams.get('cursor') ?? '0');
    const all = subjects().filter(
      (item) =>
        (!q || item.title.toLowerCase().includes(q) || item.aliases.some((alias) => alias.toLowerCase().includes(q))) &&
        (!kind || item.kind === kind) &&
        (!scope || item.scope === scope),
    );
    const items = all.slice(offset, offset + limit).map(row);
    const next = offset + limit < all.length ? String(offset + limit) : null;
    return HttpResponse.json({ revision: 1, data: { items, next_cursor: next } });
  }),
  http.get(`*${API_BASE}/knowledge/instructions`, async () => {
    const stop = await gate();
    if (stop) return stop;
    const item = find(1) as MockRecord;
    return HttpResponse.json({ revision: 1, data: detail(item) });
  }),
  http.get(`*${API_BASE}/knowledge/:id/page`, async ({ params }) => {
    const stop = await gate();
    if (stop) return stop;
    const item = find(params.id);
    if (!item) return refusal(404, 'not_found', 'No such knowledge record.');
    const page: KnowledgeSubjectPage = {
      subject: plain(item),
      kind: item.kind,
      created_at: item.created_at,
      sections: [],
      facts: store.records.filter((other) => other.facts_about === item.id && !other.retracted).map(fact),
      facts_truncated: false,
      related: [],
    };
    return HttpResponse.json({ revision: 1, data: page });
  }),
  http.get(`*${API_BASE}/knowledge/:id/links`, async ({ params }) => {
    const stop = await gate();
    if (stop) return stop;
    if (scenario().failLinks) return refusal(500, 'internal', 'The link index did not answer.');
    const id = Number(params.id);
    if (!find(id)) return refusal(404, 'not_found', 'No such knowledge record.');
    return HttpResponse.json({
      revision: 1,
      data: {
        record_id: id,
        outgoing: store.links.filter((link) => link.source === id).map(wire),
        incoming: store.links
          .filter((link) => link.target.kind === 'record' && link.target.id === String(id))
          .map(wire),
      },
    });
  }),
  http.get(`*${API_BASE}/knowledge/:id`, async ({ params }) => {
    const stop = await gate();
    if (stop) return stop;
    const item = find(params.id);
    if (!item) return refusal(404, 'not_found', 'No such knowledge record.');
    return HttpResponse.json({ revision: 1, data: detail(item) });
  }),
  http.patch(`*${API_BASE}/knowledge/:id`, async ({ params, request }) => {
    const body = (await remember(request)) as { title?: string; body?: string };
    const item = find(params.id);
    if (!item) return refusal(404, 'not_found', 'No such knowledge record.');
    const expected = request.headers.get('If-Match')?.replace(/^W\//, '').replaceAll('"', '');
    if (expected && expected !== item.updated_at) {
      return refusal(409, 'stale', 'The record changed since you read it.');
    }
    const before = { title: item.title, body: item.body, updated_at: item.updated_at };
    store.history[item.id] = [
      { id: Date.now(), at: now(), tool: 'knowledge.edit', by: 'operator', previous_body: item.body, undone_at: null },
      ...(store.history[item.id] ?? []),
    ];
    Object.assign(item, { title: body.title ?? item.title, body: body.body ?? item.body, updated_at: now() });
    const ids = effect(() => Object.assign(item, before));
    return HttpResponse.json({ data: { id: item.id, record: plain(item) }, effect_ids: ids });
  }),
  http.post(`*${API_BASE}/knowledge/:id/confirm`, async ({ params, request }) => {
    await remember(request);
    const item = find(params.id);
    if (!item) return refusal(404, 'not_found', 'No such knowledge record.');
    Object.assign(item, { confirmed: true, updated_at: now() });
    return HttpResponse.json({ data: { id: item.id, record: plain(item) } });
  }),
  http.post(`*${API_BASE}/knowledge/:id/retract`, async ({ params, request }) => {
    await remember(request);
    const item = find(params.id);
    if (!item) return refusal(404, 'not_found', 'No such knowledge record.');
    Object.assign(item, { retracted: true, updated_at: now() });
    const ids = effect(() => Object.assign(item, { retracted: false, updated_at: now() }));
    return HttpResponse.json({ data: { id: item.id, record: plain(item) }, effect_ids: ids });
  }),
  http.post(`*${API_BASE}/knowledge/:id/restore`, async ({ params, request }) => {
    await remember(request);
    const item = find(params.id);
    if (!item) return refusal(404, 'not_found', 'No such knowledge record.');
    Object.assign(item, { retracted: false, updated_at: now() });
    return HttpResponse.json({ data: { id: item.id, record: plain(item) } });
  }),
  http.post(`*${API_BASE}/knowledge/links/:linkId/retract`, async ({ params, request }) => {
    await remember(request);
    const link = store.links.find((item) => item.id === Number(params.linkId));
    if (!link || link.removed) return refusal(404, 'not_found', 'No such link.');
    link.removed = true;
    const ids = effect(() => {
      link.removed = false;
    });
    return HttpResponse.json({ data: wire(link), effect_ids: ids });
  }),
  http.post(`*${API_BASE}/knowledge/:id/links`, async ({ params, request }) => {
    const body = (await remember(request)) as {
      target_kind: 'record' | 'task' | 'space';
      target_id: string;
      relation: string;
    };
    const id = Number(params.id);
    if (!find(id)) return refusal(404, 'not_found', 'No such knowledge record.');
    const existing = store.links.find(
      (link) =>
        link.source === id &&
        !link.removed &&
        link.target.kind === body.target_kind &&
        link.target.id === body.target_id &&
        link.relation === body.relation,
    );
    if (existing) return refusal(409, 'conflict', 'These are already linked that way.');
    const link: MockLink = {
      id: nextLink++,
      source: id,
      target: { kind: body.target_kind, id: body.target_id },
      relation: body.relation,
      writer: 'operator',
      created_at: now(),
      removed: false,
    };
    store.links.push(link);
    const ids = effect(() => {
      link.removed = true;
    });
    if (scenario().loseLinks) return HttpResponse.error();
    return HttpResponse.json({ data: wire(link), effect_ids: ids }, { status: 201 });
  }),
  http.post(`*${API_BASE}/effects/:id/undo`, async ({ params, request }) => {
    const found = effects.find((item) => item.id === Number(params.id));
    if (!found) return undefined;
    await remember(request);
    if (found.done) return refusal(409, 'not_undoable', 'Nothing is left to undo.');
    found.done = true;
    found.undo();
    return HttpResponse.json({ data: { undone: true } });
  }),
];
