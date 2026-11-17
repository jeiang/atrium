import { delay, HttpResponse, http } from 'msw';
import type {
  KnowledgeDetail,
  KnowledgeFact,
  KnowledgeHistory,
  KnowledgeLink,
  KnowledgeLinkEnd,
  KnowledgeMap,
  KnowledgeRecord,
  KnowledgeSubjectPage,
  KnowledgeSubjectRow,
  Space,
  SpacePage,
} from '../contract';
import { API_BASE } from '../contract';

export interface KnowledgeScenario {
  empty?: boolean;
  fail?: boolean;
  slow?: boolean;
  failLinks?: boolean;
  loseLinks?: boolean;
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
  files: SpacePage['files'];
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
        {
          id: 602,
          name: 'garden-seeds.csv',
          bytes: 4096,
          created_at: secs(10, 9),
          media_type: 'text/csv',
          origin: 'upload',
          path: 'garden-seeds.csv',
          tokens: 900,
        },
      ],
    },
    { space: space({ id: 6, name: 'Studio move', private: true }), threads: [], tasks: [], files: [] },
  ];
}

let store = seed();
let spaces = seedSpaces();
let effects: MockEffect[] = [];
let nextLink = 200;
let nextEffect = 900;
let nextSpace = 10;
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
    files: found.files,
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
  return { record: plain(item), same_run: [], history: store.history[item.id] ?? [] };
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
    const all = scenario().empty ? [] : spaces.map((item) => item.space);
    const next = offset + limit < all.length ? String(offset + limit) : null;
    return HttpResponse.json({ items: all.slice(offset, offset + limit), next_cursor: next });
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
