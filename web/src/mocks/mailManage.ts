import { delay, HttpResponse, http } from 'msw';
import type {
  ConfirmEffect,
  MailListedRule,
  MailRuleInput,
  MailStatus,
  Preview,
  Row,
  RuleRef,
  RuleVocabulary,
} from '../contract';
import { API_BASE } from '../contract';
import type { AppsScenario } from './apps';

export interface MailManageScenario {
  status?: 'shadow' | 'live' | 'ineligible' | 'watch' | 'error';
  rules?: 'empty' | 'error';
  decisions?: 'empty' | 'error';
  confirm?: 'stale';
  command?: 'unknown';
  caller?: 'agent';
  delay?: number;
}

interface Store {
  rules: MailListedRule[];
  decisions: Row[];
  status: MailStatus;
  tokens: Map<string, string>;
  nextToken: number;
  nextRun: number;
}

const HOUR = 3600;
const DAY = 24 * HOUR;
const NOW = Date.parse('2026-11-10T09:30:00Z') / 1000;
const ACCOUNTS = ['garden-club', 'home'];
const WRITE_CAP = 25;

const fields: [string, string, boolean][] = [
  ['from_addr', 'From address', false],
  ['from_domain', 'From domain', false],
  ['from_name', 'From name', false],
  ['reply_to', 'Reply-To', false],
  ['rpath_domain', 'Return-Path domain', false],
  ['list_id', 'List-Id', false],
  ['subject', 'Subject', false],
  ['subject_tmpl', 'Subject template', false],
  ['msgid_domain', 'Message-ID domain', false],
  ['precedence', 'Precedence', false],
  ['category', 'Category', false],
  ['to', 'To', true],
  ['dkim_d', 'DKIM domain', true],
  ['esp', 'Sending service', true],
  ['att_type', 'Attachment type', true],
  ['att_name', 'Attachment name', true],
];
const ops = ['eq', 'in', 'suffix', 're', 'present', 'absent'];

export const mailVocabulary: RuleVocabulary = {
  fields: [
    ...fields.map(([name, label, list]) => ({ name, label, list, ops: [...ops] })),
    { name: 'age_days', label: 'Age in days', list: false, ops: ['gte'] },
  ],
  ops: [
    { name: 'eq', label: 'is', hint: 'Exact value, case-insensitive', value: 'text' },
    { name: 'in', label: 'is one of', hint: 'Values separated by commas', value: 'list' },
    { name: 'suffix', label: 'is or ends in domain', hint: 'Domains separated by commas', value: 'list' },
    { name: 're', label: 'matches regex', hint: 'RE2 regular expression, case-insensitive', value: 'text' },
    { name: 'present', label: 'is present', hint: '', value: 'none' },
    { name: 'absent', label: 'is absent', hint: '', value: 'none' },
    { name: 'gte', label: 'is at least', hint: 'Whole number of days', value: 'days' },
  ],
  actions: [
    { name: 'file', label: 'File into', destination: true },
    { name: 'delete', label: 'Delete' },
  ].map((item) => ({ destination: false, ...item })),
};

export function seedRules(): MailListedRule[] {
  return [
    {
      id: 'bills',
      action: 'file',
      dest: 'Bills',
      when: [{ field: 'from_domain', op: 'suffix', value: ['meadow-water.example', 'hillside-power.example'] }],
      decision_count: 24,
    },
    {
      id: 'newsletters',
      action: 'file',
      dest: 'Newsletters',
      when: [{ field: 'list_id', op: 'present' }],
      decision_count: 3,
    },
    {
      id: 'seed-catalog',
      account: 'garden-club',
      action: 'file',
      dest: 'Catalogs',
      when: [
        { field: 'from_addr', op: 'eq', value: 'catalog@thornfield-seeds.example' },
        { field: 'subject', op: 're', value: '^(spring|autumn) catalog' },
      ],
      decision_count: 2,
    },
    {
      id: 'old-promos',
      account: 'home',
      action: 'delete',
      when: [
        { field: 'subject', op: 'in', value: ['Flash sale', 'Last chance'] },
        { field: 'age_days', op: 'gte', value: 30 },
      ],
      decision_count: 0,
    },
  ];
}

const senders: [string, string, string][] = [
  ['Meadow Water', 'billing@meadow-water.example', 'Your water bill for'],
  ['Hillside Power', 'accounts@hillside-power.example', 'Statement for'],
];
const months = [
  'January',
  'February',
  'March',
  'April',
  'May',
  'June',
  'July',
  'August',
  'September',
  'October',
  'November',
  'December',
];

function decision(id: number, fields: Partial<Row> & Pick<Row, 'subject' | 'from_name' | 'from_addr'>): Row {
  const ts = NOW - id * 5 * HOUR;
  return {
    decision_id: id,
    ts,
    stage: 'operator',
    action: 'file',
    message_id: 900 + id,
    account: 'garden-club',
    date_ts: ts - 60,
    seen: true,
    flagged: false,
    agreement: 'agree',
    action_has_undo: 0,
    more: 0,
    ...fields,
  };
}

function ref(id: string, state: RuleRef['state'], title: string): RuleRef {
  return { id, state, title };
}

export function seedDecisions(): Row[] {
  const rows: Row[] = [
    decision(1, {
      subject: 'Plot 14 waiting list',
      from_name: 'Odile Marsh',
      from_addr: 'odile@garden-club.example',
      stage: 'llm',
      action: 'none',
      agreement: 'pending',
    }),
    decision(2, {
      subject: 'Spring catalog is here',
      from_name: 'Thornfield Seeds',
      from_addr: 'catalog@thornfield-seeds.example',
      dest: 'Catalogs',
      rule: ref('seed-catalog', 'changed', 'File into Catalogs'),
      agreement: 'disagree',
      corrected_to: 'Shopping',
    }),
    decision(3, {
      subject: 'Raffle tickets for the harvest fair',
      from_name: 'Fair committee',
      from_addr: 'fair@garden-club.example',
      dest: 'Events',
      rule: ref('old-raffle', 'removed', 'File into Events'),
    }),
    decision(4, {
      subject: 'Compost workshop notes',
      from_name: 'Garden club digest',
      from_addr: 'digest@garden-club.example',
      dest: 'Newsletters',
      rule: ref('newsletters', 'current', 'File into Newsletters'),
      account: 'home',
    }),
    decision(5, {
      subject: 'Hedge trimmer for sale',
      from_name: 'Bram Okafor',
      from_addr: 'bram@lanes.example',
      stage: 'knn',
      dest: 'Swaps',
      agreement: 'disagree',
      corrected_to: 'INBOX',
    }),
    decision(6, {
      subject: 'Autumn catalog preview',
      from_name: 'Thornfield Seeds',
      from_addr: 'catalog@thornfield-seeds.example',
      dest: 'Catalogs',
      rule: ref('seed-catalog', 'current', 'File into Catalogs'),
    }),
    decision(7, {
      subject: 'Your raffle ticket number',
      from_name: 'Fair committee',
      from_addr: 'fair@garden-club.example',
      dest: 'Events',
      rule: ref('old-raffle', 'removed', 'File into Events'),
    }),
    decision(8, {
      subject: 'Seed swap minutes',
      from_name: 'Garden club digest',
      from_addr: 'digest@garden-club.example',
      dest: 'Newsletters',
      rule: ref('newsletters', 'current', 'File into Newsletters'),
    }),
    decision(9, {
      subject: 'Pumpkin weigh-in results',
      from_name: 'Garden club digest',
      from_addr: 'digest@garden-club.example',
      dest: 'Newsletters',
      rule: ref('newsletters', 'current', 'File into Newsletters'),
    }),
  ];
  for (let index = 0; index < 24; index++) {
    const [name, addr, lead] = senders[index % 2] as [string, string, string];
    rows.push(
      decision(10 + index, {
        subject: `${lead} ${months[(22 - index + 120) % 12]}`,
        from_name: name,
        from_addr: addr,
        dest: 'Bills',
        account: index % 3 === 0 ? 'home' : 'garden-club',
        rule: ref('bills', 'current', 'File into Bills'),
      }),
    );
  }
  return rows;
}

const shadowCounts = {};
const liveCounts = { file: { applied: 41, failed: 1 }, flag: { applied: 12 }, trash: { applied: 6, held: 1 } };

export function seedStatus(kind: MailManageScenario['status'] = 'shadow'): MailStatus {
  const live = kind === 'live' || kind === 'watch';
  return {
    mode: live ? 'live' : 'shadow',
    paused: false,
    eligible: kind === 'shadow' || kind === undefined || kind === 'error',
    unmet: live ? ['already_live'] : kind === 'ineligible' ? ['unresolved_writes'] : [],
    accounts: ACCOUNTS,
    baseline: live ? { messages: 1240, taken_at: NOW - 2 * DAY } : { messages: 0, taken_at: null },
    shadow: { actions: live ? liveCounts : shadowCounts },
    replay: { run_id: 7, at: NOW - 3 * DAY, messages: 1802, differ: 41 },
    deletions: { pending_proposals: live ? 2 : 0 },
    watch_closely:
      kind === 'watch' ? { active: true, mode: 'live', since: NOW - 2 * DAY, until: NOW + 5 * DAY } : { active: false },
  };
}

function statement(effect: Pick<ConfirmEffect, 'action' | 'accounts' | 'baseline'>): string {
  const list = effect.accounts.join(', ');
  if (effect.action === 'rebaseline')
    return `Retakes the go-live baseline for ${list}. About ${effect.baseline} read, unflagged inbox messages become the new baseline and are never touched by the automatic inbox pass; messages left out of it can be filed, flagged or deleted on the next pass.`;
  return `Sets mail_mode to live. Automatic filing, flagging and deletion start writing to the mailboxes of ${list}, at most ${WRITE_CAP} writes per account per pass. The next pass takes the go-live baseline if none exists: about ${effect.baseline} read, unflagged inbox messages are never touched by the automatic inbox pass.`;
}

const PAUSE =
  'Pause (Assistant › Authority) stops every automatic mail write until resume; it does not change the mode.';

function error(code: string, message: string, status: number): Response {
  return HttpResponse.json({ error: { code, message } }, { status });
}

function slug(text: string): string {
  return (
    text
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-|-$/g, '') || 'rule'
  );
}

declare global {
  var atriumMailManageRequests: { method: string; path: string }[] | undefined;
}

function record(request: Request): void {
  globalThis.atriumMailManageRequests ??= [];
  globalThis.atriumMailManageRequests.push({ method: request.method, path: new URL(request.url).pathname });
}

function invalid(rule: MailRuleInput | undefined): string | undefined {
  if (!rule) return 'rule is required';
  if (!rule.when || rule.when.length === 0) return 'when needs at least one condition';
  if (rule.action === 'file' && !rule.dest) return 'dest is required for file';
  for (const item of rule.when) {
    const op = mailVocabulary.ops.find((entry) => entry.name === item.op);
    if (!op) return `unknown op ${item.op}`;
    if (
      op.value !== 'none' &&
      (item.value === undefined || item.value === '' || (Array.isArray(item.value) && item.value.length === 0))
    )
      return `${item.field} needs a value`;
  }
  return undefined;
}

export function mailManageHandlers(read: () => { mailManage?: MailManageScenario; apps?: AppsScenario }) {
  let store: Store | undefined;
  let seededFrom: string | undefined;

  const scenario = (): MailManageScenario => read().mailManage ?? {};
  const current = (): Store => {
    const key = JSON.stringify(scenario());
    if (!store || key !== seededFrom) {
      seededFrom = key;
      store = {
        rules: scenario().rules === 'empty' ? [] : seedRules(),
        decisions: scenario().decisions === 'empty' ? [] : seedDecisions(),
        status: seedStatus(scenario().status),
        tokens: new Map(),
        nextToken: 1,
        nextRun: 8,
      };
    }
    return store;
  };
  const off = (): boolean => read().apps?.off?.includes('mail') === true;
  const wait = async (): Promise<void> => {
    const ms = scenario().delay;
    if (ms) await delay(ms);
  };
  const revision = (): string => `"mail-manage-${Date.now()}"`;
  const page = <T>(items: T[], url: URL, fallback: number) => {
    const limit = Number(url.searchParams.get('limit') ?? String(fallback));
    const start = Number(url.searchParams.get('cursor') ?? '0');
    return {
      limit,
      start,
      items: items.slice(start, start + limit),
      next: start + limit < items.length ? String(start + limit) : null,
    };
  };
  const refuseCaller = (): Response | undefined =>
    scenario().caller === 'agent'
      ? error('agent_caller', 'agent sign-ins cannot change the mail mode', 403)
      : undefined;
  const titleOf = (rule: Pick<MailListedRule, 'action' | 'dest'>): string =>
    rule.action === 'file' ? `File into ${rule.dest}` : rule.action === 'delete' ? 'Delete' : 'Leave in the inbox';

  return [
    http.get(`*${API_BASE}/mail/rules/vocabulary`, async () => {
      await wait();
      return HttpResponse.json({ data: mailVocabulary, revision: revision() });
    }),
    http.get(`*${API_BASE}/mail/rules`, async ({ request }) => {
      await wait();
      if (off()) return error('not_found', 'This app is off.', 404);
      if (scenario().rules === 'error') return error('unavailable', 'The rules file could not be read.', 503);
      const url = new URL(request.url);
      const account = url.searchParams.get('account');
      const rules = current().rules.filter((rule) => !account || !rule.account || rule.account === account);
      const { items, next } = page(rules, url, 100);
      return HttpResponse.json({ data: { items, next_cursor: next }, revision: revision() });
    }),
    http.post(`*${API_BASE}/mail/rules/preview`, async ({ request }) => {
      await wait();
      const body = (await request.json()) as { rule?: MailRuleInput };
      const problem = invalid(body.rule);
      if (problem) return error('invalid', problem, 422);
      const count = 3 + (body.rule?.when.length ?? 0) * 4;
      const sample = current()
        .decisions.slice(0, 3)
        .map((row) => ({
          id: row.message_id,
          account: row.account,
          folder: 'INBOX',
          from_name: row.from_name,
          from_addr: row.from_addr,
          subject: row.subject,
          date_ts: row.date_ts,
        }));
      const data: Preview = { count, in_inbox: 2, total: 500, sample };
      return HttpResponse.json({ data, revision: revision() });
    }),
    http.post(`*${API_BASE}/mail/rules`, async ({ request }) => {
      record(request);
      await wait();
      const body = (await request.json()) as { rule?: MailRuleInput };
      const problem = invalid(body.rule);
      if (problem || !body.rule) return error('invalid', problem ?? 'rule is required', 422);
      const s = current();
      let id = slug(body.rule.dest ?? body.rule.action);
      while (s.rules.some((rule) => rule.id === id)) id = `${id}-2`;
      const made: MailListedRule = { ...body.rule, id, decision_count: 0 };
      s.rules.push(made);
      return HttpResponse.json({ data: { ...body.rule, id } }, { status: 201 });
    }),
    http.patch(`*${API_BASE}/mail/rules/:id`, async ({ params, request }) => {
      record(request);
      await wait();
      const s = current();
      const old = s.rules.find((rule) => rule.id === params.id);
      if (!old) return error('not_found', 'Unknown rule', 404);
      const body = (await request.json()) as { rule?: MailRuleInput };
      const problem = invalid(body.rule);
      if (problem || !body.rule) return error('invalid', problem ?? 'rule is required', 422);
      const next: MailListedRule = { ...body.rule, id: old.id, decision_count: old.decision_count };
      s.rules = s.rules.map((rule) => (rule.id === old.id ? next : rule));
      for (const row of s.decisions) if (row.rule && row.rule.id === next.id) row.rule.state = 'changed';
      return HttpResponse.json({ data: { ...body.rule, id: next.id } });
    }),
    http.delete(`*${API_BASE}/mail/rules/:id`, async ({ params, request }) => {
      record(request);
      await wait();
      const s = current();
      const before = s.rules.length;
      s.rules = s.rules.filter((rule) => rule.id !== params.id);
      if (s.rules.length === before) return error('not_found', 'Unknown rule', 404);
      for (const row of s.decisions) if (row.rule && row.rule.id === params.id) row.rule.state = 'removed';
      return HttpResponse.json({ data: { deleted: true } });
    }),
    http.get(`*${API_BASE}/mail/rules/:id/decisions`, async ({ params, request }) => {
      await wait();
      if (off()) return error('not_found', 'This app is off.', 404);
      if (scenario().decisions === 'error') return error('unavailable', 'The decision log could not be read.', 503);
      const url = new URL(request.url);
      const limit = Number(url.searchParams.get('limit') ?? '50');
      if (!(limit >= 1 && limit <= 100)) return error('invalid', 'limit must be 1..100', 422);
      const s = current();
      const id = String(params.id);
      const rule = s.rules.find((item) => item.id === id);
      const rows = s.decisions.filter((row) => row.rule?.id === id);
      if (!rule && rows.length === 0) return error('not_found', 'Unknown rule', 404);
      const cursor = url.searchParams.get('cursor');
      if (cursor !== null && !/^\d+$/.test(cursor)) return error('bad_cursor', 'the cursor is not from this list', 400);
      const { items, next } = page(rows, url, 50);
      const state: RuleRef['state'] = !rule
        ? 'removed'
        : rows.some((row) => row.rule?.state === 'changed')
          ? 'changed'
          : 'current';
      const title = rule ? titleOf(rule) : (rows[0]?.rule?.title ?? undefined);
      return HttpResponse.json({
        data: { rule: { id, state, title }, items, total: rows.length, next_cursor: next },
        revision: revision(),
      });
    }),
    http.get(`*${API_BASE}/mail/triage`, async ({ request }) => {
      await wait();
      if (off()) return error('not_found', 'This app is off.', 404);
      if (scenario().decisions === 'error') return error('unavailable', 'The decision log could not be read.', 503);
      const url = new URL(request.url);
      const q = (url.searchParams.get('q') ?? '').toLowerCase();
      const account = url.searchParams.get('account');
      const agreement = url.searchParams.get('agreement');
      const rows = current().decisions.filter(
        (row) =>
          (!q || `${row.subject} ${row.from_name} ${row.from_addr}`.toLowerCase().includes(q)) &&
          (!account || row.account === account) &&
          (!agreement || row.agreement === agreement),
      );
      const { items, next } = page(rows, url, 50);
      const check = current().decisions.filter((row) => row.agreement === 'disagree' || row.agreement === 'pending');
      return HttpResponse.json({
        data: { items, check: check.slice(0, 5), total: rows.length, next_cursor: next },
        revision: revision(),
      });
    }),
    http.get(`*${API_BASE}/mail/status`, async () => {
      await wait();
      if (off()) return error('not_found', 'This app is off.', 404);
      if (scenario().status === 'error') return error('unavailable', 'The mail status could not be read.', 503);
      return HttpResponse.json({ data: current().status, revision: revision() });
    }),
    http.post(`*${API_BASE}/mail/status/confirm`, async ({ request }) => {
      record(request);
      await wait();
      const refused = refuseCaller();
      if (refused) return refused;
      const body = (await request.json()) as { action?: string };
      const s = current();
      if (body.action !== 'go_live' && body.action !== 'rebaseline')
        return error('invalid', 'action must be go_live or rebaseline', 422);
      if (body.action === 'go_live' && !s.status.eligible)
        return error(
          'not_eligible',
          `Go-live is not possible now: ${s.status.unmet.join(', ') || 'already live'}`,
          409,
        );
      const token = `confirm-${s.nextToken++}`;
      s.tokens.set(token, body.action);
      const base = { action: body.action, accounts: s.status.accounts, baseline: 1240 };
      const data: ConfirmEffect = {
        ...base,
        confirm_token: token,
        expires_at: Date.now() / 1000 + 600,
        write_cap: WRITE_CAP,
        statement: statement(base),
        pause: PAUSE,
      };
      return HttpResponse.json({ data });
    }),
    http.post(`*${API_BASE}/mail/status/go-live`, async ({ request }) => {
      record(request);
      await wait();
      if (scenario().command === 'unknown') return HttpResponse.error();
      const refused = refuseCaller();
      if (refused) return refused;
      const body = (await request.json()) as { confirm_token?: string };
      const s = current();
      if (!body.confirm_token) return error('confirm_required', 'confirm the effect first', 409);
      if (scenario().confirm === 'stale' || s.tokens.get(body.confirm_token) !== 'go_live')
        return error('confirm_stale', 'the confirmation expired or the state changed', 409);
      s.tokens.delete(body.confirm_token);
      if (!s.status.eligible) return error('not_eligible', 'Go-live is not possible now', 409);
      const now = Date.now() / 1000;
      s.status = {
        ...s.status,
        mode: 'live',
        eligible: false,
        unmet: ['already_live'],
        watch_closely: { active: true, mode: 'live', since: now, until: now + 7 * DAY },
      };
      return HttpResponse.json({ data: { mode: 'live', accounts: s.status.accounts } });
    }),
    http.post(`*${API_BASE}/mail/status/rebaseline`, async ({ request }) => {
      record(request);
      await wait();
      if (scenario().command === 'unknown') return HttpResponse.error();
      const refused = refuseCaller();
      if (refused) return refused;
      const body = (await request.json()) as { confirm_token?: string };
      const s = current();
      if (!body.confirm_token) return error('confirm_required', 'confirm the effect first', 409);
      if (scenario().confirm === 'stale' || s.tokens.get(body.confirm_token) !== 'rebaseline')
        return error('confirm_stale', 'the confirmation expired or the state changed', 409);
      s.tokens.delete(body.confirm_token);
      s.status = { ...s.status, baseline: { messages: 1240, taken_at: Date.now() / 1000 } };
      return HttpResponse.json({
        data: { outcome: 'ok', baseline: 1240, created: s.status.accounts, accounts: s.status.accounts },
      });
    }),
    http.post(`*${API_BASE}/mail/status/replay`, async ({ request }) => {
      record(request);
      await delay(scenario().delay ?? 400);
      if (scenario().command === 'unknown') return HttpResponse.error();
      const refused = refuseCaller();
      if (refused) return refused;
      const s = current();
      const run = { run_id: s.nextRun++, at: Date.now() / 1000, messages: 1820, differ: 37 };
      s.status = { ...s.status, replay: run };
      return HttpResponse.json({ data: run });
    }),
  ];
}
