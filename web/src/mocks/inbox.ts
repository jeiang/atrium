import { delay, HttpResponse, http, type JsonBodyType } from 'msw';
import type { Approval, Draft, PendingBody, ProposalDetail, QueueItem } from '../contract';
import { API_BASE } from '../contract';
import { planAttention } from './plan';

export interface InboxScenario {
  decide?: 'ok' | 'conflict' | 'stale' | 'network' | 'unknown';
  empty?: boolean;
  fail?: boolean;
  mix?: boolean;
  pageDelay?: number;
  pageSize?: number;
  readDelay?: number;
  stuck?: boolean;
  unconfigured?: boolean;
}

interface MockResolution {
  id: number;
  outcome: string;
  effective_outcome: string;
  actor: string;
  source: string;
  prior_state: string;
  prior_error: string;
  ts: number;
}

interface MockEffect {
  id: number;
  proposal_id: number;
  direction: 'forward' | 'undo';
  state: string;
  blocked: boolean;
  undo_blocked: boolean;
  undoable: boolean;
  undone_at: number | null;
  resolution?: MockResolution;
}

export interface InboxRequest {
  method: string;
  path: string;
  key: string | null;
}

const NOW = 1_795_600_000;
const log = globalThis as typeof globalThis & { atriumInboxRequests?: InboxRequest[] };

const letter = [
  'Hello Ilse,',
  '',
  'Thank you for the plans for the shared garden. Sam and I read them over the weekend and we like the idea of moving the compost bins behind the shed.',
  '',
  'Two questions before the meeting on Thursday:',
  '',
  '1. Would the new raised beds keep the path wide enough for the wheelbarrow?',
  '2. Could the tap move to the north fence, so the hose reaches every bed?',
  '',
  'If both work, we are happy to help build the beds on the first Saturday of December. We can bring the drill and two spades.',
  '',
  'The seed order is also nearly ready. We picked the early carrots, the striped beans and the yellow courgettes from the list you shared. If you want anything else, add it to the sheet by Sunday evening.',
  '',
  'See you on Thursday,',
  'Sam',
].join('\n');

function effect(id: number, proposal: number, extra: Partial<MockEffect> = {}): MockEffect {
  return {
    id,
    proposal_id: proposal,
    direction: 'forward',
    state: 'done',
    blocked: false,
    undo_blocked: false,
    undoable: false,
    undone_at: null,
    ...extra,
  };
}

function stuckProposal(id: number, status: string, title: string, label: string): ProposalDetail {
  return {
    id,
    kind: 'garden_watering',
    title,
    status,
    actions: [],
    undoable: false,
    blocked: true,
    created_at: NOW - 900 + id,
    decided_at: NOW - 800 + id,
    payload: {
      kind: 'action',
      label,
      module: 'garden',
      proposal_kind: 'garden_watering',
      args: [{ label: 'Zone', value: 'Vegetable beds' }],
      preview: null,
      result: null,
      change: null,
    },
  };
}

function seed(state: InboxScenario = {}): {
  proposals: ProposalDetail[];
  drafts: Draft[];
  effects: MockEffect[];
  questions: number[];
} {
  const stuck = state.stuck === true;
  return {
    questions: [42],
    proposals: [
      {
        id: 12,
        kind: 'calendar_update',
        title: 'Move the dentist visit to Friday',
        status: 'pending',
        account: 'Home calendar',
        actions: ['approve', 'deny'],
        undoable: false,
        blocked: false,
        created_at: NOW - 5400,
        payload: {
          kind: 'action',
          label: 'Update event',
          module: 'calendar',
          proposal_kind: 'calendar_update',
          args: [{ label: 'Calendar', value: 'Home' }],
          preview: null,
          result: null,
          change: {
            kind: 'calendar_update',
            calendar: 'Home',
            uid: 'evt-2291',
            title: 'Dentist check-up',
            start: '2026-11-27T09:30:00+01:00',
            end: '2026-11-27T10:00:00+01:00',
            location: 'Riverside Dental, Mill Lane 4',
          },
        },
      } as ProposalDetail,
      {
        id: 14,
        kind: 'weather_station_reset',
        title: 'Reset the garden weather station',
        status: 'pending',
        actions: ['approve', 'deny'],
        undoable: true,
        blocked: false,
        created_at: NOW - 1800,
        payload: {
          kind: 'action',
          label: 'Reset station',
          module: 'garden',
          proposal_kind: 'weather_station_reset',
          args: [
            { label: 'Station', value: 'Garden shed' },
            { label: 'Keep history', value: 'Yes' },
          ],
          preview: 'The station restarts and keeps its readings.',
          result: null,
          change: null,
        },
      },
      ...(stuck
        ? [
            stuckProposal(15, 'failed', 'Start watering the vegetable beds', 'Start watering'),
            stuckProposal(16, 'applied', 'Stop the shed heater schedule', 'Stop schedule'),
          ]
        : []),
    ],
    drafts: [
      {
        id: 7,
        account: 'sam@example.test',
        account_configured: state.unconfigured !== true,
        to: 'Ilse de Wit <ilse@example.test>',
        cc: '',
        subject: 'Shared garden plans',
        body: letter,
        status: 'draft',
        revision: 3,
        created_at: NOW - 7200,
        updated_at: NOW - 3600,
        unexpected_recipients: [],
      },
      ...(stuck
        ? [
            {
              id: 8,
              account: 'sam@example.test',
              account_configured: true,
              to: 'Joris Bakker <joris@example.test>',
              cc: '',
              subject: 'Allotment key handover',
              body: 'Hi Joris,\n\nThe key is under the blue pot.\n\nSam',
              status: 'unknown',
              revision: 2,
              created_at: NOW - 2400,
              updated_at: NOW - 1200,
              unexpected_recipients: [],
            } satisfies Draft,
          ]
        : []),
    ],
    effects: stuck
      ? [
          effect(601, 15, { state: 'unknown', blocked: true }),
          effect(602, 16, { undo_blocked: true }),
          effect(603, 16, { direction: 'undo', state: 'unknown', blocked: true }),
        ]
      : [],
  };
}

let store = seed();
let read = '';
let off = new Set<string>();

function scenario(): InboxScenario {
  try {
    const parsed: { inbox?: InboxScenario; modules?: Record<string, string> } = JSON.parse(
      localStorage.getItem('atrium.mock') ?? '{}',
    );
    const key = JSON.stringify(parsed.inbox ?? {});
    if (key !== read) {
      read = key;
      store = seed(parsed.inbox);
    }
    off = new Set(Object.entries(parsed.modules ?? {}).flatMap(([name, state]) => (state === 'off' ? [name] : [])));
    return parsed.inbox ?? {};
  } catch {
    return {};
  }
}

function row(item: ProposalDetail): Approval {
  const payload = item.payload.kind === 'action' ? item.payload : undefined;
  return {
    id: item.id,
    kind: item.kind,
    kind_label: payload?.label ?? item.kind,
    title: item.title,
    args: payload?.args ?? [],
    preview: payload?.preview ?? null,
    result: null,
    status: item.status,
    undoable: item.undoable,
    blocked: item.blocked,
    blocked_effect: false,
    blocked_undo: false,
    created_at: item.created_at,
    module: payload?.module ?? 'core',
    approval: true,
  };
}

function waiting(): { proposals: ProposalDetail[]; drafts: Draft[] } {
  if (scenario().empty) return { proposals: [], drafts: [] };
  return {
    proposals: store.proposals.filter((item) => item.status === 'pending' || item.blocked),
    drafts: store.drafts.filter((item) => item.status === 'draft' || item.status === 'unknown'),
  };
}

function extras(): QueueItem[] {
  return [
    ...store.questions.map(
      (thread): QueueItem => ({
        id: `question:${thread}`,
        kind: 'ask',
        title: 'Which room should the garden meeting use?',
        summary: 'The assistant is waiting: the library room seats twelve, the community hall seats forty.',
        source: { type: 'question', id: String(thread) },
        arrived_at: NOW - 600,
        waiting_for: 'answer',
        module: 'assistant',
      }),
    ),
    {
      id: 'event_candidate:9001',
      kind: 'attention',
      title: 'Seed swap on Saturday at ten',
      summary: 'Found in mail from the garden club',
      source: { type: 'event_candidate', id: '9001' },
      arrived_at: NOW - 3000,
      waiting_for: 'review',
      module: 'calendar',
    },
    ...planAttention(),
  ];
}

const rank = (item: QueueItem): number =>
  item.waiting_for === 'answer' || item.waiting_for === 'permission' ? 0 : item.due ? 1 : 2;

export function queue(): QueueItem[] {
  const state = scenario();
  const open = waiting();
  const items: QueueItem[] = [
    ...open.proposals.map((item): QueueItem => {
      const approval = row(item);
      return {
        id: `proposal:${item.id}`,
        kind: 'proposal',
        title: item.title,
        summary: item.blocked ? `${approval.kind_label}: Outcome unknown` : approval.kind_label,
        source: { type: 'proposal', id: String(item.id) },
        arrived_at: item.created_at,
        waiting_for: item.blocked ? 'resolve' : 'approval',
        module: approval.module,
      };
    }),
    ...open.drafts.map(
      (item): QueueItem => ({
        id: `draft:${item.id}`,
        kind: 'draft',
        title: item.subject || '(no subject)',
        summary: item.status === 'unknown' ? `Send outcome unknown. To ${item.to}` : `To ${item.to}`,
        source: { type: 'draft', id: String(item.id) },
        arrived_at: item.created_at,
        waiting_for: item.status === 'unknown' ? 'resolve' : 'send',
        module: 'mail',
      }),
    ),
    ...(state.mix && !state.empty ? extras() : []),
  ];
  return items
    .filter((item) => !off.has(item.module))
    .sort((a, b) => rank(a) - rank(b) || (a.due ?? 0) - (b.due ?? 0) || a.arrived_at - b.arrived_at);
}

export function inboxCount(): number {
  return queue().length;
}

function decide(key: string, approve: boolean): void {
  const [type, raw] = key.split(':');
  const id = Number(raw);
  const at = Math.floor(Date.now() / 1000);
  if (type === 'draft') {
    const draft = store.drafts.find((item) => item.id === id);
    if (draft?.status !== 'draft') return;
    Object.assign(draft, approve ? { status: 'sent', sent_at: at } : { status: 'discarded' }, { updated_at: at });
    return;
  }
  const proposal = store.proposals.find((item) => item.id === id);
  if (proposal?.status !== 'pending') return;
  Object.assign(proposal, { status: approve ? 'applied' : 'dismissed', decided_at: at });
  if (approve && proposal.undoable) {
    store.effects.push(effect(500 + store.effects.length, id, { undoable: true }));
  }
}

export function decideElsewhere(key: string): void {
  scenario();
  if (key.startsWith('question:')) store.questions = store.questions.filter((id) => `question:${id}` !== key);
  else decide(key, true);
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

function command(key: string, approve: boolean, request: Request, body: () => JsonBodyType): Response {
  const mode = scenario().decide ?? 'ok';
  record(request);
  if (mode === 'conflict') return refusal(409, 'conflict', 'The item changed while you were looking at it.');
  if (mode === 'stale') return refusal(412, 'precondition_failed', 'The draft has a newer revision.');
  const open = waiting();
  const [type, raw] = key.split(':');
  const id = Number(raw);
  const pending =
    type === 'draft'
      ? open.drafts.some((item) => item.id === id && item.status === 'draft')
      : open.proposals.some((item) => item.id === id && item.status === 'pending');
  if (!pending) return refusal(409, 'not_pending', 'It was already decided.');
  if (mode === 'unknown') return unconfirmed(type, id);
  decide(key, approve);
  if (mode === 'network') return HttpResponse.error();
  return HttpResponse.json(body());
}

function unconfirmed(type: string | undefined, id: number): Response {
  const at = Math.floor(Date.now() / 1000);
  if (type === 'draft') {
    const draft = store.drafts.find((item) => item.id === id);
    if (draft) Object.assign(draft, { status: 'unknown', updated_at: at });
    return refusal(409, 'effect_unknown', 'The mail server did not confirm the send.');
  }
  const proposal = store.proposals.find((item) => item.id === id);
  if (proposal) {
    Object.assign(proposal, { status: 'failed', blocked: true, decided_at: at });
    store.effects.push(effect(500 + store.effects.length, id, { state: 'unknown', blocked: true }));
  }
  return refusal(409, 'effect_unknown', 'The change did not confirm whether it happened.');
}

function proposal(id: unknown): ProposalDetail | undefined {
  return store.proposals.find((item) => item.id === Number(id));
}

function draft(id: unknown): Draft | undefined {
  return store.drafts.find((item) => item.id === Number(id));
}

const forwardOutcomes: Record<string, string> = { applied: 'done', 'not-applied': 'failed', partial: 'partial' };
const undoOutcomes: Record<string, string> = { undone: 'done', 'not-undone': 'failed', partial: 'partial' };

function resolveProposal(item: ProposalDetail, outcome: string): Response | undefined {
  const blocked = store.effects.filter((entry) => entry.proposal_id === item.id && entry.blocked);
  if (!item.blocked || blocked.length === 0)
    return refusal(409, 'not_resolved', `proposal ${item.id} has no write with an unknown outcome`);
  const misfit = blocked.find((entry) => !(entry.direction === 'undo' ? undoOutcomes : forwardOutcomes)[outcome]);
  if (misfit) return refusal(422, 'invalid', `outcome ${outcome} does not fit a ${misfit.direction} write`);
  const at = Math.floor(Date.now() / 1000);
  for (const entry of blocked) {
    const effective = (entry.direction === 'undo' ? undoOutcomes : forwardOutcomes)[outcome] ?? outcome;
    entry.resolution = {
      id: 900 + entry.id,
      outcome,
      effective_outcome: effective,
      actor: 'operator',
      source: 'web',
      prior_state: entry.state,
      prior_error: '',
      ts: at,
    };
    entry.state = effective;
    entry.blocked = false;
    if (entry.direction === 'undo') {
      for (const forward of store.effects)
        if (forward.proposal_id === item.id && forward.direction === 'forward') forward.undo_blocked = false;
      if (effective === 'done') item.undone_at = at;
    }
  }
  item.blocked = false;
  return undefined;
}

function resolveCommand(request: Request, apply: () => Response | undefined, body: () => JsonBodyType): Response {
  const mode = scenario().decide ?? 'ok';
  record(request);
  if (mode === 'conflict') return refusal(409, 'conflict', 'The item changed while you were looking at it.');
  const refused = apply();
  if (refused) return refused;
  if (mode === 'network') return HttpResponse.error();
  return HttpResponse.json(body());
}

export const inboxHandlers = [
  http.get(`*${API_BASE}/queue`, async ({ request }) => {
    const state = scenario();
    const params = new URL(request.url).searchParams;
    const offset = Number(params.get('cursor') ?? 0);
    if (state.readDelay) await delay(state.readDelay);
    if (offset > 0 && state.pageDelay) await delay(state.pageDelay);
    if (state.fail) return refusal(500, 'internal', 'The review store did not answer.');
    const all = queue();
    const kind = params.get('kind');
    const counts = { ask: 0, proposal: 0, draft: 0, attention: 0 };
    for (const item of all) counts[item.kind] += 1;
    const matching = kind ? all.filter((item) => item.kind === kind) : all;
    const end = state.pageSize ? offset + state.pageSize : matching.length;
    return HttpResponse.json({
      data: {
        items: matching.slice(offset, end),
        total: all.length,
        counts,
        next_cursor: end < matching.length ? String(end) : undefined,
      },
      revision: 1,
    });
  }),
  http.get(`*${API_BASE}/queue/:id`, ({ params }) => {
    const item = queue().find((entry) => entry.id === params.id);
    if (!item) return refusal(404, 'not_found', 'It no longer waits on you.');
    return HttpResponse.json({ data: item, revision: 1 });
  }),
  http.get(`*${API_BASE}/pending`, () => {
    if (scenario().fail) return refusal(500, 'internal', 'The review store did not answer.');
    const open = waiting();
    const body: PendingBody = {
      approvals: open.proposals.filter((item) => !item.blocked).map(row),
      blocked: open.proposals.filter((item) => item.blocked).map(row),
      drafts: open.drafts,
      counts: {
        approvals: open.proposals.filter((item) => !item.blocked).length,
        blocked: open.proposals.filter((item) => item.blocked).length,
        drafts: open.drafts.filter((item) => item.status === 'draft').length,
        unknown: open.drafts.filter((item) => item.status === 'unknown').length,
      },
    };
    return HttpResponse.json(body);
  }),
  http.get(`*${API_BASE}/proposals/:id`, ({ params }) => {
    scenario();
    const item = proposal(params.id);
    if (!item) return refusal(404, 'not_found', 'No such proposal.');
    return HttpResponse.json({ data: item, revision: 1 });
  }),
  http.get(`*${API_BASE}/drafts/:id`, ({ params }) => {
    scenario();
    const item = draft(params.id);
    if (!item) return refusal(404, 'not_found', 'No such draft.');
    return HttpResponse.json(item);
  }),
  http.post(`*${API_BASE}/approvals/:id/approve`, ({ params, request }) =>
    command(`proposal:${params.id}`, true, request, () => {
      const item = proposal(params.id) as ProposalDetail;
      return { approval: row(item), outcome: 'applied', summary: 'Done.' };
    }),
  ),
  http.post(`*${API_BASE}/approvals/:id/deny`, ({ params, request }) =>
    command(`proposal:${params.id}`, false, request, () => ({ approval: row(proposal(params.id) as ProposalDetail) })),
  ),
  http.post(`*${API_BASE}/drafts/:id/send`, ({ params, request }) =>
    command(`draft:${params.id}`, true, request, () => ({ draft: draft(params.id), filed: true, refused: [] })),
  ),
  http.post(`*${API_BASE}/drafts/:id/discard`, ({ params, request }) =>
    command(`draft:${params.id}`, false, request, () => ({ draft: draft(params.id) })),
  ),
  http.post(`*${API_BASE}/approvals/:id/resolve`, async ({ params, request }) => {
    const { outcome } = (await request.clone().json()) as { outcome?: string };
    const item = proposal(params.id);
    if (!item) return refusal(404, 'not_found', 'No such proposal.');
    return resolveCommand(
      request,
      () => resolveProposal(item, outcome ?? ''),
      () => ({ approval: row(item) }),
    );
  }),
  http.post(`*${API_BASE}/drafts/:id/resolve`, async ({ params, request }) => {
    const { outcome } = (await request.clone().json()) as { outcome?: string };
    const item = draft(params.id);
    if (!item) return refusal(404, 'not_found', 'No such draft.');
    return resolveCommand(
      request,
      () => {
        if (outcome !== 'sent' && outcome !== 'draft')
          return refusal(422, 'invalid', 'an unknown draft can only be marked sent or returned to draft');
        if (item.status !== 'unknown') return refusal(409, 'not_resolved', 'draft is not in the unknown state');
        const at = Math.floor(Date.now() / 1000);
        Object.assign(item, {
          status: outcome,
          sent_at: outcome === 'sent' ? at : item.sent_at,
          revision: item.revision + 1,
          updated_at: at,
        });
        return undefined;
      },
      () => ({ draft: item }),
    );
  }),
  http.get(`*${API_BASE}/effects`, ({ request }) => {
    scenario();
    const id = Number(new URL(request.url).searchParams.get('proposal_id'));
    return HttpResponse.json({ items: store.effects.filter((item) => item.proposal_id === id) });
  }),
  http.post(`*${API_BASE}/effects/:id/undo`, ({ params, request }) => {
    record(request);
    const effect = store.effects.find((item) => item.id === Number(params.id));
    if (!effect || effect.undone_at) return refusal(409, 'not_undoable', 'Nothing is left to undo.');
    effect.undone_at = Math.floor(Date.now() / 1000);
    const item = proposal(effect.proposal_id);
    if (item) item.undone_at = effect.undone_at;
    return HttpResponse.json({ undone: true });
  }),
];

export const inboxAskHandlers = [
  http.post(`*${API_BASE}/threads/:thread/answer`, ({ params, request }) => {
    const state = scenario();
    if (!state.mix) return undefined;
    record(request);
    if (state.decide === 'conflict' || !store.questions.includes(Number(params.thread))) {
      return refusal(409, 'no_question', 'There is no question to answer.');
    }
    store.questions = store.questions.filter((id) => id !== Number(params.thread));
    return HttpResponse.json({ run_id: 77 }, { status: 202 });
  }),
];
