import { delay, HttpResponse, http } from 'msw';
import type { Account, Draft, Loop, MailMessage, MailSearchHit, QueueItem } from '../contract';
import { API_BASE } from '../contract';
import type { AppsScenario } from './apps';

export interface MailboxScenario {
  messages?: 'empty' | 'error' | 'many';
  refuse?: string;
  send?: 'unknown' | 'refused';
  action?: 'unknown';
  delay?: number;
}

interface Row {
  hit: MailSearchHit;
  body: string;
  cc?: string;
  inbox: boolean;
  attachments: MailMessage['attachments'];
}

interface Effect {
  id: number;
  undo: () => void;
  undone: boolean;
}

interface Store {
  rows: Row[];
  drafts: Draft[];
  effects: Effect[];
  nextDraft: number;
  nextEffect: number;
}

const ME = 'Wren Halloway <wren@garden-club.example>';
const DAY = 86_400_000;
const NOW = Date.parse('2026-11-10T09:30:00Z');

export const mailAccountsSeed: Account[] = [account('garden-club', 'Garden club'), account('home', 'Home')];

const loops: Loop[] = [
  {
    id: 41,
    thread_key: 'thread-seed-swap',
    account: 'garden-club',
    state: 'owe_reply',
    last_message_id: 101,
    since: (NOW - 3 * DAY) / 1000,
    counterpart: 'Odile Marsh',
    subject: 'Seed swap on Saturday',
    dismissed: 0,
    days: 3,
    snoozed_until: null,
  },
];

const refusals: Record<string, string> = {
  shadow_mode: 'mail_mode is not live; message actions need live mode',
  write_blocked: 'an earlier write to this message is unresolved; resolve it on the Activity page first',
  no_archive_folder: 'the account has no archive folder',
  message_changed: 'the message changed or moved on the server; sync and try again',
};

function account(id: string, label: string): Account {
  return {
    id,
    label,
    login: `${id}@garden-club.example`,
    enabled: true,
    auth_method: 'password',
    password_set: true,
    pinned: false,
    preset: 'imap',
    reason: '',
    state: 'ok',
  };
}

function row(
  id: number,
  from: string,
  subject: string,
  body: string,
  ago: number,
  extra: Partial<MailSearchHit> & { files?: MailMessage['attachments']; inbox?: boolean } = {},
): Row {
  const { files = [], inbox = true, ...hit } = extra;
  return {
    hit: {
      message_id: id,
      account: 'garden-club',
      folder: inbox ? 'INBOX' : 'Archive',
      from,
      to: ME,
      subject,
      snippet: body.replace(/\s+/g, ' ').slice(0, 140),
      date: new Date(NOW - ago).toISOString(),
      seen: true,
      flagged: false,
      has_attachment: files.length > 0,
      ...hit,
    },
    body,
    inbox,
    attachments: files,
  };
}

export function seedMailbox(scenario: MailboxScenario = {}): Row[] {
  if (scenario.messages === 'empty') return [];
  const rows = [
    row(
      101,
      'Odile Marsh <odile@seedswap.example>',
      'Seed swap on Saturday',
      'Hello Wren,\n\nCould you bring the bean seeds you saved last autumn? We start at ten by the greenhouse.\n\nOdile',
      2 * 3_600_000,
      { seen: false },
    ),
    row(
      102,
      'Allotment office <office@allotments.example>',
      'Plot 14 renewal',
      'Your plot renewal is due by 30 November. The form and the map are attached.',
      DAY,
      {
        flagged: true,
        files: [
          { filename: 'renewal-form.pdf', content_type: 'application/pdf', size: 184_320 },
          { filename: 'plot-map.png', content_type: 'image/png', size: 92_160 },
        ],
      },
    ),
    row(
      103,
      'Teo Lindqvist <teo@lindqvist.example>',
      'Pruning workshop photos',
      'The photos from the pruning workshop are up. Thanks for leading the apple session.',
      2 * DAY,
    ),
    row(
      104,
      'Bramble Books <orders@bramble.example>',
      'Your order has shipped',
      'Order 5521 left the shop this morning: A Year in the Orchard.',
      3 * DAY,
      { account: 'home', seen: false },
    ),
    row(
      105,
      'Mina Okafor <mina@okafor.example>',
      'Compost rota',
      'Swapping my week for yours works. I will turn the heaps on the 18th.',
      5 * DAY,
      { inbox: false },
    ),
  ];
  if (scenario.messages !== 'many') return rows;
  for (let index = 0; index < 40; index += 1) {
    rows.push(
      row(
        200 + index,
        'Garden club list <list@garden-club.example>',
        `Weekly notes ${index + 1}`,
        'Notes.',
        (6 + index) * DAY,
      ),
    );
  }
  return rows;
}

function error(code: string, message: string, status: number, effects?: number[]): Response {
  return HttpResponse.json({ error: { code, message, ...(effects ? { effect_ids: effects } : {}) } }, { status });
}

function message(item: Row): MailMessage {
  const { hit } = item;
  return {
    id: hit.message_id,
    account: hit.account,
    folder: hit.folder,
    subject: hit.subject,
    from_addr: hit.from.replace(/^.*<([^>]+)>$/, '$1'),
    from_name: hit.from.replace(/\s*<[^>]+>$/, ''),
    headers: {
      From: hit.from,
      To: String(hit.to),
      ...(item.cc ? { Cc: item.cc } : {}),
      Subject: hit.subject,
      Date: new Date(hit.date).toUTCString(),
    },
    date_ts: Date.parse(hit.date) / 1000,
    body: item.body,
    body_source: 'raw',
    seen: hit.seen,
    flagged: hit.flagged,
    in_inbox: item.inbox,
    attachments: item.attachments,
    location: hit.folder,
    corrected_to: null,
    decision: null as unknown as MailMessage['decision'],
    flag_decision: null as unknown as MailMessage['flag_decision'],
  };
}

function quote(text: string): string {
  return text
    .split('\n')
    .map((line) => `> ${line}`)
    .join('\n');
}

declare global {
  var atriumMailRequests: { method: string; path: string; key: string | null }[] | undefined;
}

function record(request: Request): void {
  globalThis.atriumMailRequests ??= [];
  globalThis.atriumMailRequests.push({
    method: request.method,
    path: new URL(request.url).pathname,
    key: request.headers.get('Idempotency-Key'),
  });
}

export function mailHandlers(read: () => { mailbox?: MailboxScenario; apps?: AppsScenario }) {
  let store: Store = { rows: [], drafts: [], effects: [], nextDraft: 500, nextEffect: 5000 };
  let seededFrom: string | undefined;

  const scenario = (): MailboxScenario => read().mailbox ?? {};
  const current = (): Store => {
    const key = JSON.stringify(scenario());
    if (key !== seededFrom) {
      seededFrom = key;
      store = { rows: seedMailbox(scenario()), drafts: [], effects: [], nextDraft: 500, nextEffect: 5000 };
    }
    return store;
  };
  const off = (): boolean => read().apps?.off?.includes('mail') === true;
  const wait = async (): Promise<void> => {
    const ms = scenario().delay;
    if (ms) await delay(ms);
  };
  const find = (id: unknown): Row | undefined => current().rows.find((item) => item.hit.message_id === Number(id));
  const findDraft = (id: unknown): Draft | undefined => current().drafts.find((item) => item.id === Number(id));
  const revision = (): string => `"mail-${Date.now()}"`;

  const change = (item: Row, action: string, apply: () => () => void): Response => {
    const refuse = scenario().refuse;
    if (refuse) return error(refuse, refusals[refuse] ?? 'refused', 409);
    const s = current();
    const id = s.nextEffect++;
    if (scenario().action === 'unknown') return error('write_outcome_unknown', 'the outcome is unknown', 409, [id]);
    const undo = apply();
    s.effects.push({ id, undo, undone: false });
    return HttpResponse.json({
      data: { action, message_id: item.hit.message_id, ...(action === 'archive' ? { folder: 'Archive' } : {}) },
      effect_ids: [id],
      undoable: true,
    });
  };

  const makeDraft = (fields: Partial<Draft> & Pick<Draft, 'account' | 'to' | 'subject' | 'body'>): Draft => {
    const s = current();
    const at = Date.now() / 1000;
    const made: Draft = {
      id: s.nextDraft++,
      cc: '',
      status: 'draft',
      revision: 1,
      created_at: at,
      updated_at: at,
      sent_at: null,
      smtp_result: null,
      thread_id: null,
      in_reply_to_message_id: null,
      unexpected_recipients: [],
      account_configured: true,
      ...fields,
    };
    s.drafts.push(made);
    return made;
  };

  return [
    http.get(`*${API_BASE}/mail/accounts`, async () => {
      await wait();
      if (off()) return error('not_found', 'This app is off.', 404);
      return HttpResponse.json({ data: { items: mailAccountsSeed, next_cursor: null }, revision: revision() });
    }),
    http.get(`*${API_BASE}/mail/messages`, async ({ request }) => {
      await wait();
      if (off()) return error('not_found', 'This app is off.', 404);
      if (scenario().messages === 'error') return error('unavailable', 'The mail index could not be read.', 503);
      const url = new URL(request.url);
      const q = (url.searchParams.get('q') ?? '').toLowerCase();
      const accountId = url.searchParams.get('account');
      const folder = url.searchParams.get('folder');
      const limit = Number(url.searchParams.get('limit') ?? '30');
      const start = Number(url.searchParams.get('cursor') ?? '0');
      const matches = current().rows.filter(
        ({ hit, body }) =>
          (!q || `${hit.subject} ${hit.from} ${body}`.toLowerCase().includes(q)) &&
          (!accountId || hit.account === accountId) &&
          (!folder || hit.folder.toLowerCase() === folder.toLowerCase()),
      );
      const page = matches.slice(start, start + limit);
      const next = start + limit < matches.length ? String(start + limit) : null;
      return HttpResponse.json({
        data: { items: page.map((item) => item.hit), next_cursor: next, semantic: false },
        revision: revision(),
      });
    }),
    http.get(`*${API_BASE}/mail/messages/:id`, async ({ params }) => {
      await wait();
      const item = find(params.id);
      if (!item) return error('not_found', 'Unknown message', 404);
      return HttpResponse.json({ data: message(item), revision: revision() });
    }),
    http.post(`*${API_BASE}/mail/messages/:id/archive`, async ({ params, request }) => {
      record(request);
      await wait();
      const item = find(params.id);
      if (!item) return error('not_found', 'Unknown message', 404);
      if (!item.inbox) return error('not_in_inbox', 'the message is not in the inbox', 409);
      return change(item, 'archive', () => {
        item.inbox = false;
        item.hit.folder = 'Archive';
        return () => {
          item.inbox = true;
          item.hit.folder = 'INBOX';
        };
      });
    }),
    http.post(`*${API_BASE}/mail/messages/:id/read`, async ({ params, request }) => {
      record(request);
      const { read: seen } = (await request.clone().json()) as { read: boolean };
      await wait();
      const item = find(params.id);
      if (!item) return error('not_found', 'Unknown message', 404);
      return change(item, seen ? 'read' : 'unread', () => {
        const was = item.hit.seen;
        item.hit.seen = seen;
        return () => {
          item.hit.seen = was;
        };
      });
    }),
    http.post(`*${API_BASE}/mail/messages/:id/flag`, async ({ params, request }) => {
      record(request);
      const { flagged } = (await request.clone().json()) as { flagged: boolean };
      await wait();
      const item = find(params.id);
      if (!item) return error('not_found', 'Unknown message', 404);
      return change(item, flagged ? 'flag' : 'unflag', () => {
        const was = item.hit.flagged;
        item.hit.flagged = flagged;
        return () => {
          item.hit.flagged = was;
        };
      });
    }),
    http.post(`*${API_BASE}/undo/:entry`, ({ params, request }) => {
      const id = Number(String(params.entry).replace(/^write:/, ''));
      const found = current().effects.find((item) => item.id === id);
      if (!found) return undefined;
      record(request);
      if (found.undone) return HttpResponse.json({ undone: false, error: 'Nothing is left to undo.' });
      found.undo();
      found.undone = true;
      return HttpResponse.json({ undone: true });
    }),
    http.post(`*${API_BASE}/effects/:id/resolve`, ({ params, request }) => {
      if (Number(params.id) < 5000) return undefined;
      record(request);
      return HttpResponse.json({ effect: null });
    }),
    http.get(`*${API_BASE}/mail/loops`, () =>
      HttpResponse.json({ data: { items: off() ? [] : loops, next_cursor: null }, revision: revision() }),
    ),
    http.get(`*${API_BASE}/queue/:id`, ({ params }) => {
      const key = String(params.id);
      const loop = loops.find((item) => `loop:${item.thread_key}` === key);
      if (!loop) return undefined;
      const item: QueueItem = {
        id: key,
        kind: 'attention',
        title: loop.subject,
        summary: loop.counterpart,
        source: { type: 'loop', id: loop.thread_key },
        arrived_at: loop.since,
        waiting_for: 'due',
        module: 'mail',
        loop_id: loop.id,
      };
      return HttpResponse.json({ data: item, revision: revision() });
    }),
    http.post(`*${API_BASE}/mail/messages/:id/reply`, ({ params, request }) => {
      record(request);
      const item = find(params.id);
      if (!item) return error('not_found', 'Unknown message', 404);
      return HttpResponse.json(
        makeDraft({
          account: item.hit.account,
          to: item.hit.from,
          subject: `Re: ${item.hit.subject}`,
          body: `\n\n${quote(item.body)}`,
          in_reply_to_message_id: item.hit.message_id,
        }),
      );
    }),
    http.post(`*${API_BASE}/mail/messages/:id/forward`, async ({ params, request }) => {
      record(request);
      const { to, cc } = (await request.clone().json()) as { to: string; cc?: string };
      const item = find(params.id);
      if (!item) return error('not_found', 'Unknown message', 404);
      if (!to?.trim()) return error('invalid_draft', 'a recipient is required', 422);
      return HttpResponse.json(
        makeDraft({
          account: item.hit.account,
          to,
          cc: cc ?? '',
          subject: `Fwd: ${item.hit.subject}`,
          body: `\n\n---------- Forwarded message ---------\nFrom: ${item.hit.from}\nSubject: ${item.hit.subject}\n\n${item.body}`,
        }),
      );
    }),
    http.post(`*${API_BASE}/drafts`, async ({ request }) => {
      record(request);
      const input = (await request.clone().json()) as {
        account: string;
        to: string;
        cc?: string;
        subject?: string;
        body: string;
      };
      if (!input.to?.trim() || !input.body?.trim()) return error('invalid_draft', 'to and body are required', 422);
      return HttpResponse.json(
        makeDraft({
          account: input.account,
          to: input.to,
          cc: input.cc ?? '',
          subject: input.subject ?? '',
          body: input.body,
        }),
      );
    }),
    http.get(`*${API_BASE}/drafts/:id`, ({ params }) => {
      const item = findDraft(params.id);
      return item ? HttpResponse.json({ data: item, revision: revision() }) : undefined;
    }),
    http.patch(`*${API_BASE}/drafts/:id`, async ({ params, request }) => {
      const item = findDraft(params.id);
      if (!item) return undefined;
      record(request);
      const input = (await request.clone().json()) as Partial<Draft>;
      if (input.revision !== undefined && input.revision !== item.revision)
        return error('stale', 'the draft changed', 409);
      if (item.status !== 'draft') return error('stale', 'the draft is not editable', 409);
      Object.assign(item, {
        to: input.to ?? item.to,
        cc: input.cc ?? item.cc,
        subject: input.subject ?? item.subject,
        body: input.body ?? item.body,
        revision: item.revision + 1,
        updated_at: Date.now() / 1000,
      });
      return HttpResponse.json(item);
    }),
    http.post(`*${API_BASE}/drafts/:id/send`, async ({ params, request }) => {
      const item = findDraft(params.id);
      if (!item) return undefined;
      record(request);
      await wait();
      const { revision: rev } = (await request.clone().json()) as { revision?: number };
      if (rev !== item.revision) return error('stale', 'the draft changed', 409);
      const send = scenario().send;
      if (send === 'refused') return error('not_sent', 'the mail server refused the message', 409);
      if (send === 'unknown') {
        item.status = 'unknown';
        return error('send_outcome_unknown', 'the send outcome is unknown', 409);
      }
      Object.assign(item, { status: 'sent', sent_at: Date.now() / 1000, revision: item.revision + 1 });
      return HttpResponse.json({ draft: item, filed: 'Sent Messages', refused: [] });
    }),
    http.post(`*${API_BASE}/drafts/:id/discard`, ({ params, request }) => {
      const item = findDraft(params.id);
      if (!item) return undefined;
      record(request);
      item.status = 'discarded';
      return HttpResponse.json({ draft: item });
    }),
    http.post(`*${API_BASE}/drafts/:id/resolve`, async ({ params, request }) => {
      const item = findDraft(params.id);
      if (!item) return undefined;
      record(request);
      const { outcome } = (await request.clone().json()) as { outcome?: string };
      if (item.status !== 'unknown') return error('not_resolved', 'draft is not in the unknown state', 409);
      Object.assign(item, {
        status: outcome === 'sent' ? 'sent' : 'draft',
        revision: item.revision + 1,
        sent_at: outcome === 'sent' ? Date.now() / 1000 : null,
      });
      return HttpResponse.json({ draft: item });
    }),
  ];
}
