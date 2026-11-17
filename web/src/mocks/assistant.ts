import { delay, HttpResponse, http } from 'msw';
import type {
  Model,
  ModelConfiguration,
  PermissionRule,
  ProposalItem,
  ScheduleJob,
  ScheduleRun,
  Spend,
  SubagentRole,
} from '../contract';
import { API_BASE } from '../contract';

export interface AssistantScenario {
  fail?: 'spend' | 'schedules' | 'permissions' | 'configuration' | 'roles' | 'requests' | 'runs';
  empty?: boolean;
  spend?: 'near' | 'exhausted';
  runNow?: 'down' | 'refused';
  scheduleElsewhere?: boolean;
  permissionsPinned?: boolean;
}

export interface AssistantRequest {
  method: string;
  path: string;
  ifMatch: string | null;
  idempotencyKey: string | null;
  body: unknown;
}

const log = globalThis as typeof globalThis & { atriumAssistantRequests?: AssistantRequest[] };
const EVENTS = 'atrium-mock-events';
const HOUR = 3_600_000;
const GRAMMAR =
  "unrecognised schedule; use an ISO datetime, 'daily HH:MM', 'weekly DOW HH:MM' or a 5-field cron expression";
const CHANNELS = ['own', 'notify', 'people', 'internet'];
const SITUATIONS = ['clean', 'kinds', 'pii', 'untrusted_knowledge', 'untrusted_kinds', 'untrusted_isolated'];
const DEFAULTS = [
  ['allow', 'allow', 'allow', 'allow', 'allow', 'allow'],
  ['allow', 'allow', 'allow', 'allow', 'allow', 'allow'],
  ['ask', 'ask', 'ask', 'ask', 'ask', 'ask'],
  ['allow', 'ask', 'deny', 'ask', 'deny', 'allow'],
];
const FLOORS = [
  ['allow', 'allow', 'allow', 'allow', 'allow', 'allow'],
  ['allow', 'allow', 'allow', 'allow', 'allow', 'allow'],
  ['allow', 'allow', 'allow', 'ask', 'ask', 'allow'],
  ['allow', 'allow', 'allow', 'ask', 'ask', 'allow'],
];
const RANK: Record<string, number> = { allow: 0, ask: 1, deny: 2 };
const clockFormat = new Intl.DateTimeFormat('en-US', { weekday: 'short', hour: '2-digit', minute: '2-digit' });

const rates = (input: number, output: number) => ({
  input,
  output,
  cache_read: input / 10,
  cache_write_5m: input * 1.25,
  cache_write_1h: input * 2,
});

export const models: Model[] = [
  {
    id: 'claude-sonnet-5-5',
    name: 'Sonnet 5.5',
    context_window: 1_000_000,
    efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
    forced_tools: false,
    max_output: 64_000,
    rates: rates(3, 15),
    run_cap_usd: 10,
    thinking: [],
  },
  {
    id: 'claude-opus-5-5',
    name: 'Opus 5.5',
    context_window: 1_000_000,
    efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
    forced_tools: false,
    max_output: 64_000,
    rates: rates(15, 75),
    run_cap_usd: 10,
    thinking: [],
  },
  {
    id: 'claude-haiku-5-5',
    name: 'Haiku 5.5',
    context_window: 200_000,
    efforts: ['low', 'medium', 'high'],
    forced_tools: false,
    max_output: 32_000,
    rates: rates(1, 5),
    run_cap_usd: 10,
    thinking: [],
  },
];

const roles: SubagentRole[] = [
  {
    name: 'researcher',
    description: 'Looks up facts across your knowledge and the web, then reports back with sources.',
    effort: 'medium',
    prompt: 'Answer the question you were given. Cite every source. Stop when you have enough.',
    tools: ['kb_search', 'search_web', 'web_fetch'],
  },
  {
    name: 'mail scout',
    description: 'Reads recent mail threads for one question and summarizes what it found.',
    effort: 'low',
    prompt: 'Search the mail for the question. Quote only what you need.',
    tools: ['mail_search', 'open_loops'],
  },
];

interface Defaults {
  model: string;
  effort: string;
  context_limit: number;
}

interface Store {
  defaults: Defaults;
  permissions: Record<string, string>;
  permissionRevision: number;
  jobs: ScheduleJob[];
  runs: Record<number, ScheduleRun[]>;
  nextJob: number;
  nextRun: number;
  elsewhereUsed: boolean;
}

function iso(offset: number): string {
  return new Date(Date.now() + offset).toISOString();
}

function clock(offset: number): string {
  return clockFormat.format(new Date(Date.now() + offset));
}

function job(fields: Partial<ScheduleJob> & Pick<ScheduleJob, 'id' | 'name' | 'schedule' | 'prompt'>): ScheduleJob {
  return {
    kind: 'cron',
    created_by: 'operator',
    source: 'operator',
    protected: false,
    enabled: true,
    cadence: cadence(fields.schedule),
    revision: '1',
    next_run_at: iso(5 * HOUR),
    next_due_text: clock(5 * HOUR),
    last_run_at: null,
    last_started_text: null,
    last_run_id: null,
    last_status: null,
    last_error: null,
    ...fields,
  };
}

function seed(state: AssistantScenario): Store {
  const jobs = state.empty
    ? []
    : [
        job({
          id: 1,
          name: 'Morning brief',
          schedule: 'daily 07:00',
          prompt: 'Write the morning brief.',
          source: 'builtin',
          created_by: 'operator',
          protected: true,
          last_run_id: 140,
          last_status: 'ok',
          last_started_text: clock(-17 * HOUR),
          last_run_at: iso(-17 * HOUR),
        }),
        job({
          id: 2,
          name: 'Nightly review',
          schedule: 'daily 03:00',
          prompt: 'Review the day and tidy knowledge.',
          kind: 'nightly',
          source: 'builtin',
          last_run_id: 139,
          last_status: 'skipped_budget',
          last_started_text: clock(-21 * HOUR),
          last_run_at: iso(-21 * HOUR),
        }),
        job({
          id: 3,
          name: 'Garden club newsletter',
          schedule: 'weekly mon 09:00',
          prompt: 'Summarize the garden club newsletter and list any dates.',
          last_run_id: 141,
          last_status: 'running',
          last_started_text: clock(-0.1 * HOUR),
          last_run_at: iso(-0.1 * HOUR),
        }),
        job({
          id: 4,
          name: 'Parcel watch',
          schedule: 'every 6h',
          prompt: 'Check the parcel tracking pages and tell me about changes.',
          created_by: 'agent',
          source: 'agent',
          enabled: false,
          last_run_id: 120,
          last_status: 'error',
          last_error: 'tracking page timed out',
          last_started_text: clock(-50 * HOUR),
          last_run_at: iso(-50 * HOUR),
        }),
      ];
  const runs: Record<number, ScheduleRun[]> = {
    1: [
      { id: 140, job_name: 'Morning brief', status: 'ok', started_text: clock(-17 * HOUR), cost: 0.21, thread_id: 7 },
      { id: 130, job_name: 'Morning brief', status: 'ok', started_text: clock(-41 * HOUR), cost: 0.19, thread_id: 7 },
    ],
    2: [{ id: 139, job_name: 'Nightly review', status: 'skipped_budget', started_text: clock(-21 * HOUR), cost: 0 }],
    3: [
      { id: 141, job_name: 'Garden club newsletter', status: 'running', started_text: clock(-0.1 * HOUR), cost: 0.02 },
    ],
    4: [
      {
        id: 120,
        job_name: 'Parcel watch',
        status: 'error',
        started_text: clock(-50 * HOUR),
        cost: 0.04,
        error: 'tracking page timed out',
      },
    ],
  };
  return {
    defaults: { model: 'claude-sonnet-5-5', effort: 'high', context_limit: 200_000 },
    permissions: {},
    permissionRevision: 4,
    jobs,
    runs,
    nextJob: 5,
    nextRun: 142,
    elsewhereUsed: false,
  };
}

let store = seed({});
let scenarioText = '';

function scenario(read: () => { assistant?: AssistantScenario }): AssistantScenario {
  const state = read().assistant ?? {};
  const text = JSON.stringify(state);
  if (text !== scenarioText) {
    scenarioText = text;
    store = seed(state);
  }
  return state;
}

function cadence(schedule: string): string {
  const text = schedule.trim();
  let match = /^daily (\d\d):(\d\d)$/.exec(text);
  if (match) return `Every day at ${match[1]}:${match[2]}`;
  match = /^weekly (mon|tue|wed|thu|fri|sat|sun) (\d\d):(\d\d)$/.exec(text);
  if (match) return `Every ${match[1]} at ${match[2]}:${match[3]}`;
  match = /^every (\d+)(m|h)$/.exec(text);
  if (match) return `Every ${match[1]} ${match[2] === 'h' ? 'hours' : 'minutes'}`;
  if (/^(\S+\s+){4}\S+$/.test(text)) return `Cron ${text}`;
  if (!Number.isNaN(Date.parse(text))) return `Once at ${text}`;
  return '';
}

function refusal(status: number, code: string, message: string, extra: Record<string, unknown> = {}): Response {
  return HttpResponse.json({ error: { code, message, ...extra } }, { status });
}

function publish(topic: string): void {
  const channel = new BroadcastChannel(EVENTS);
  channel.postMessage({ topic });
  channel.close();
}

async function record(request: Request): Promise<unknown> {
  const text = await request.clone().text();
  const body = text ? (JSON.parse(text) as unknown) : null;
  log.atriumAssistantRequests ??= [];
  log.atriumAssistantRequests.push({
    method: request.method,
    path: new URL(request.url).pathname,
    ifMatch: request.headers.get('If-Match'),
    idempotencyKey: request.headers.get('Idempotency-Key'),
    body,
  });
  return body;
}

function failed(state: AssistantScenario, part: AssistantScenario['fail']): Response | undefined {
  return state.fail === part ? refusal(500, 'internal', 'atrium hit an internal error.') : undefined;
}

function rules(state: AssistantScenario): PermissionRule[] {
  return CHANNELS.flatMap((channel, c) =>
    SITUATIONS.map((situation, s) => {
      const key = `permission_${channel}_${situation}`;
      const stored = store.permissions[key];
      const value = stored ?? DEFAULTS[c]?.[s] ?? 'allow';
      const floor = FLOORS[c]?.[s] ?? 'allow';
      const pinned = state.permissionsPinned && key === 'permission_internet_clean';
      const restricted = (c === 2 || c === 3) && (s === 3 || s === 4);
      return {
        key,
        channel,
        situation,
        value,
        floor,
        effective: (RANK[value] ?? 0) >= (RANK[floor] ?? 0) ? value : floor,
        choices: restricted ? ['ask', 'deny'] : ['allow', 'ask', 'deny'],
        source: pinned ? 'pin' : stored === undefined ? 'default' : 'instance',
      };
    }),
  );
}

const requests: ProposalItem[] = [
  {
    id: 311,
    kind: 'permission',
    status: 'pending',
    title: 'Fetch a page from parcels.example.test',
    actions: [],
    undoable: false,
  },
  {
    id: 305,
    kind: 'permission',
    status: 'approved',
    title: 'Reply to Dana about the plot rota',
    actions: [],
    undoable: false,
  },
  {
    id: 298,
    kind: 'permission',
    status: 'dismissed',
    title: 'Post the meeting notes to forum.example.test',
    actions: [],
    undoable: false,
  },
];

function spend(state: AssistantScenario): Spend {
  const now = new Date();
  const start = new Date(now.getFullYear(), now.getMonth(), 1).getTime() / 1000;
  const next = new Date(now.getFullYear(), now.getMonth() + 1, 1).getTime() / 1000;
  const budget = 150;
  const spent = state.spend === 'exhausted' ? 150 : state.spend === 'near' ? 124.5 : 42.18;
  const committed = state.spend ? 0 : 0.35;
  const unattended = budget * 0.8;
  const interactiveRefused = state.spend === 'exhausted';
  const unattendedRefused = state.spend !== undefined;
  const empty = state.empty === true;
  return {
    month: now.toISOString().slice(0, 7),
    month_start: start,
    next_month: next,
    budget: {
      budget_usd: budget,
      spent_usd: empty ? 0 : spent,
      committed_usd: empty ? 0 : committed,
      reserved_usd: 0,
      unknown_usd: 0,
      expired_usd: 0,
      carry_in_usd: 0,
      remaining_usd: empty ? budget : Math.max(0, budget - spent - committed),
      share: 0.8,
      interactive_ceiling_usd: budget,
      unattended_ceiling_usd: unattended,
      next_month: next,
    },
    state: interactiveRefused ? 'exhausted' : unattendedRefused ? 'near' : 'ok',
    headroom: [
      {
        class: 'interactive',
        ceiling_usd: budget,
        headroom_usd: interactiveRefused ? 0 : budget - spent,
        refused: interactiveRefused,
      },
      {
        class: 'unattended',
        ceiling_usd: unattended,
        headroom_usd: unattendedRefused ? 0 : unattended - spent,
        refused: unattendedRefused,
      },
    ],
    caps: [
      { key: 'monthly_budget_usd', scope: 'month', unit: 'usd', value: budget, source: 'default', editable: true },
      { key: 'max_run_cost_usd', scope: 'run', unit: 'usd', value: 10, source: 'default', editable: true },
      { key: 'thread_cost_cap_usd', scope: 'thread', unit: 'usd', value: 10, source: 'default', editable: false },
      {
        key: 'dreaming_max_cost_usd',
        scope: 'dreaming_run',
        unit: 'usd',
        value: 0.3,
        source: 'default',
        editable: false,
      },
      {
        key: 'dreaming_monthly_usd',
        scope: 'dreaming_month',
        unit: 'usd',
        value: 3,
        source: 'default',
        editable: false,
      },
      { key: 'ops_worker_max_run_usd', scope: 'ops_run', unit: 'usd', value: 0.1, source: 'default', editable: false },
      { key: 'ops_worker_monthly_usd', scope: 'ops_month', unit: 'usd', value: 5, source: 'default', editable: false },
    ],
    by_class: empty
      ? []
      : [
          { key: 'interactive', calls: 212, cost_usd: 34.9, committed_usd: committed },
          { key: 'unattended', calls: 61, cost_usd: 7.28, committed_usd: 0 },
        ],
    by_kind: empty
      ? []
      : [
          { key: 'chat', calls: 190, cost_usd: 31.4, committed_usd: committed },
          { key: 'cron', calls: 44, cost_usd: 5.12, committed_usd: 0 },
          { key: 'sub', calls: 22, cost_usd: 3.5, committed_usd: 0 },
          { key: 'nightly', calls: 17, cost_usd: 2.16, committed_usd: 0 },
        ],
    by_model: empty
      ? []
      : [
          { key: 'claude-sonnet-5-5', calls: 230, cost_usd: 36.02, committed_usd: committed },
          { key: 'claude-haiku-5-5', calls: 43, cost_usd: 6.16, committed_usd: 0 },
        ],
    by_day: [],
    top_runs: empty
      ? []
      : [
          {
            run_id: 118,
            thread_id: 12,
            title: 'Plan the allotment rota for spring',
            kind: 'chat',
            model: 'claude-sonnet-5-5',
            started_at: start + 3 * 86_400,
            calls: 14,
            cost_usd: 2.41,
            committed_usd: 0,
          },
          {
            run_id: 140,
            thread_id: 7,
            job_id: 1,
            title: 'Morning brief',
            kind: 'cron',
            model: 'claude-sonnet-5-5',
            started_at: start + 5 * 86_400,
            calls: 6,
            cost_usd: 0.21,
            committed_usd: 0,
          },
        ],
    topics: ['assistant', 'settings'],
  };
}

function findJob(id: unknown): ScheduleJob | undefined {
  return store.jobs.find((item) => item.id === Number(id));
}

function bump(item: ScheduleJob): void {
  item.revision = String(Number(item.revision) + 1);
}

// What GET /assistant/configuration answers: the models, the stored defaults
// and the composer for those defaults.
export function configuration(): ModelConfiguration {
  const defaults = { ...store.defaults, thinking: 'adaptive', web_search: true };
  return {
    models,
    defaults,
    efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
    context_limits: [200_000, 1_000_000],
    composer: {
      ...defaults,
      cliff: false,
      coding: false,
      context: 0,
      context_limits: [200_000, 1_000_000],
      effort_fixed: false,
      efforts: ['low', 'medium', 'high'].map((value) => ({ Value: value, Disabled: false })),
      known: true,
      live: 0,
      models,
      thinking_options: [],
    },
  };
}

export function assistantHandlers(read: () => { assistant?: AssistantScenario }) {
  return [
    http.get(`*${API_BASE}/assistant/configuration`, async () => {
      const state = scenario(read);
      await delay(150);
      const fail = failed(state, 'configuration');
      if (fail) return fail;
      return HttpResponse.json(configuration());
    }),
    http.put(`*${API_BASE}/assistant/defaults`, async ({ request }) => {
      scenario(read);
      const body = (await record(request)) as Partial<Defaults> | null;
      await delay(250);
      const model = models.find((item) => item.id === body?.model);
      if (!model)
        return refusal(422, 'invalid', 'Unknown model.', {
          fields: [{ type: 'invalid', loc: ['body', 'model'], msg: 'Unknown model.' }],
        });
      if (!body?.effort || !model.efforts.includes(body.effort)) {
        return refusal(422, 'invalid', `${model.name} does not offer that effort.`, {
          fields: [{ type: 'invalid', loc: ['body', 'effort'], msg: `${model.name} does not offer that effort.` }],
        });
      }
      store.defaults = { model: model.id, effort: body.effort, context_limit: Number(body.context_limit) };
      publish('settings');
      return HttpResponse.json({ data: store.defaults, audit_id: 801 });
    }),
    http.get(`*${API_BASE}/assistant/subagent-roles`, async () => {
      const state = scenario(read);
      await delay(150);
      return failed(state, 'roles') ?? HttpResponse.json({ items: state.empty ? [] : roles });
    }),
    http.get(`*${API_BASE}/spend`, async () => {
      const state = scenario(read);
      await delay(150);
      return failed(state, 'spend') ?? HttpResponse.json(spend(state), { headers: { ETag: 'W/"spend-1"' } });
    }),
    http.get(`*${API_BASE}/permissions`, async () => {
      const state = scenario(read);
      await delay(150);
      const fail = failed(state, 'permissions');
      if (fail) return fail;
      return HttpResponse.json({
        data: { rules: rules(state), pending_requests: state.empty ? 0 : 1 },
        revision: String(store.permissionRevision),
      });
    }),
    http.put(`*${API_BASE}/permissions`, async ({ request }) => {
      const state = scenario(read);
      const body = (await record(request)) as { changes?: { key: string; value: string }[] } | null;
      await delay(250);
      const changes = body?.changes ?? [];
      if (changes.length === 0) return refusal(422, 'invalid', 'Name at least one permission.');
      const current = rules(state);
      for (const change of changes) {
        const rule = current.find((item) => item.key === change.key);
        if (!rule) return refusal(422, 'invalid', `Unknown permission ${change.key}.`);
        if (rule.source === 'pin') return refusal(409, 'refused', `${change.key} is pinned by the host`);
        if (change.value !== '' && !rule.choices.includes(change.value)) {
          const message = `${change.key}: must be one of ${rule.choices.join(', ')}`;
          return refusal(422, 'invalid', message, {
            fields: [{ type: 'invalid', loc: ['body', 'changes'], msg: message }],
          });
        }
      }
      for (const change of changes) {
        if (change.value === '') delete store.permissions[change.key];
        else store.permissions[change.key] = change.value;
      }
      store.permissionRevision += 1;
      publish('settings');
      return HttpResponse.json({ data: { keys: changes.map((change) => change.key) }, audit_id: 802 });
    }),
    http.get(`*${API_BASE}/proposals`, async ({ request }) => {
      const url = new URL(request.url);
      if (url.searchParams.get('kind') !== 'permission') return undefined;
      const state = scenario(read);
      await delay(150);
      return (
        failed(state, 'requests') ??
        HttpResponse.json({ data: { items: state.empty ? [] : requests, next_cursor: null }, revision: '9' })
      );
    }),
    http.get(`*${API_BASE}/schedules`, async () => {
      const state = scenario(read);
      await delay(150);
      return failed(state, 'schedules') ?? HttpResponse.json({ items: store.jobs, tz: 'America/Port_of_Spain' });
    }),
    http.post(`*${API_BASE}/schedules`, async ({ request }) => {
      scenario(read);
      const body = (await record(request)) as { name?: string; prompt?: string; schedule?: string } | null;
      await delay(250);
      const text = body?.schedule?.trim() ?? '';
      const words = cadence(text);
      if (!words)
        return refusal(422, 'invalid', GRAMMAR, {
          fields: [{ type: 'invalid', loc: ['body', 'schedule'], msg: GRAMMAR }],
        });
      const made = job({
        id: store.nextJob++,
        name: body?.name?.trim() ?? '',
        prompt: body?.prompt?.trim() ?? '',
        schedule: text,
        next_run_at: iso(9 * HOUR),
        next_due_text: clock(9 * HOUR),
      });
      store.jobs.push(made);
      store.runs[made.id] = [];
      publish('jobs');
      return HttpResponse.json({ id: made.id, enabled: true, job: made }, { status: 201 });
    }),
    http.patch(`*${API_BASE}/schedules/:id`, async ({ request, params }) => {
      const state = scenario(read);
      const body = (await record(request)) as { name?: string; prompt?: string; schedule?: string } | null;
      await delay(250);
      const item = findJob(params.id);
      if (!item) return refusal(404, 'not_found', 'No such schedule.');
      if (state.scheduleElsewhere && !store.elsewhereUsed) {
        store.elsewhereUsed = true;
        item.prompt = `${item.prompt} Also list the plant sale dates.`;
        bump(item);
        publish('jobs');
      }
      const ifMatch = request.headers.get('If-Match');
      if (ifMatch && ifMatch.replace(/^W\//, '').replaceAll('"', '') !== item.revision) {
        return refusal(412, 'precondition_failed', 'The schedule changed since it was read; reload it and try again.');
      }
      if (body?.schedule !== undefined) {
        const words = cadence(body.schedule);
        if (!words)
          return refusal(422, 'invalid', GRAMMAR, {
            fields: [{ type: 'invalid', loc: ['body', 'schedule'], msg: GRAMMAR }],
          });
        item.schedule = body.schedule.trim();
        item.cadence = words;
      }
      if (body?.prompt !== undefined) {
        if (item.source === 'builtin') return refusal(409, 'refused', 'Built-in schedules keep their instructions.');
        item.prompt = body.prompt;
      }
      if (body?.name !== undefined) item.name = body.name;
      bump(item);
      publish('jobs');
      return HttpResponse.json({ id: item.id, enabled: item.enabled, job: item });
    }),
    http.put(`*${API_BASE}/schedules/:id/enabled`, async ({ request, params }) => {
      scenario(read);
      const body = (await record(request)) as { enabled?: boolean } | null;
      await delay(250);
      const item = findJob(params.id);
      if (!item) return refusal(404, 'not_found', 'No such schedule.');
      if (item.protected && body?.enabled === false) {
        return refusal(409, 'refused', `${item.name} is protected and cannot be turned off`);
      }
      item.enabled = body?.enabled === true;
      bump(item);
      publish('jobs');
      return HttpResponse.json({ id: item.id, enabled: item.enabled, job: item });
    }),
    http.post(`*${API_BASE}/schedules/:id/run`, async ({ request, params }) => {
      const state = scenario(read);
      await record(request);
      await delay(250);
      const item = findJob(params.id);
      if (!item) return refusal(404, 'not_found', 'No such schedule.');
      if (state.runNow === 'down') {
        return refusal(503, 'unavailable', 'atrium serve is not running on this database; start it to run jobs');
      }
      if (state.runNow === 'refused') return refusal(409, 'refused', 'the assistant app is off');
      if (item.last_status === 'running') {
        return refusal(409, 'conflict', `${item.name} is already running (run ${item.last_run_id})`);
      }
      const run = store.nextRun++;
      item.last_run_id = run;
      item.last_status = 'running';
      item.last_started_text = clock(0);
      item.last_run_at = iso(0);
      store.runs[item.id] = [
        { id: run, job_name: item.name, status: 'running', started_text: clock(0), cost: 0 },
        ...(store.runs[item.id] ?? []),
      ];
      publish('jobs');
      return HttpResponse.json({ run, job: item.id, status: 'requested' }, { status: 202 });
    }),
    http.get(`*${API_BASE}/schedules/:id/runs`, async ({ params }) => {
      const state = scenario(read);
      await delay(150);
      return failed(state, 'runs') ?? HttpResponse.json({ items: store.runs[Number(params.id)] ?? [] });
    }),
    http.delete(`*${API_BASE}/schedules/:id`, async ({ request, params }) => {
      scenario(read);
      await record(request);
      await delay(250);
      const item = findJob(params.id);
      if (!item) return refusal(404, 'not_found', 'No such schedule.');
      if (item.source === 'builtin' || item.protected) return refusal(409, 'refused', `${item.name} is built in`);
      store.jobs = store.jobs.filter((other) => other.id !== item.id);
      publish('jobs');
      return new HttpResponse(null, { status: 204 });
    }),
  ];
}
