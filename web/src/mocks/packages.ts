import { delay, HttpResponse, http } from 'msw';
import type { Package, PackageAdd } from '../contract';
import { API_BASE } from '../contract';
import type { AppsScenario } from './apps';

export interface PackagesScenario {
  list?: 'empty' | 'error' | 'many';
  ship24?: 'missing';
  quota?: 'spent';
  refuse?: { code: string; message: string; status?: number };
  unknown?: boolean;
  busy?: boolean;
  delay?: number;
}

const LIMIT = 10;
const now = Math.floor(Date.UTC(2026, 10, 16, 15, 0) / 1000);
const HOUR = 3600;

function item(fields: Partial<Package> & Pick<Package, 'id' | 'tracking_number' | 'status'>): Package {
  return {
    aggregator: '',
    carrier: '',
    dismissed: 0,
    first_seen: now - 72 * HOUR,
    last_event: '',
    last_event_description: '',
    last_event_ts: 0,
    last_message_id: 0,
    last_sync: '',
    last_sync_ts: 0,
    merchant: '',
    merchant_key: '',
    order_ref: '',
    source_message_id: 0,
    status_text: '',
    tracker_id: '',
    updated: '',
    updated_ts: now - 6 * HOUR,
    ...fields,
  };
}

export function seedPackages(scenario: PackagesScenario): Package[] {
  if (scenario.list === 'empty') return [];
  const base = [
    item({
      id: 1,
      merchant: 'Juniper Outfitters',
      carrier: 'UPS',
      tracking_number: '1Z999AA10123456784',
      status: 'in_transit',
      status_text: 'In transit',
      tracker_id: 'trk_juniper',
      aggregator: 'ship24',
      source_message_id: 101,
      last_event_description: 'Departed the sorting center in Riverton',
      last_event_ts: now - 5 * HOUR,
    }),
    item({
      id: 2,
      merchant: 'Lark and Loom',
      carrier: 'USPS',
      tracking_number: '9400111202555842761234',
      status: 'shipped',
      status_text: 'Shipped',
      source_message_id: 102,
    }),
    item({
      id: 3,
      merchant: 'Fernhill Garden Supply',
      carrier: 'FedEx',
      tracking_number: '612999934567',
      status: 'exception',
      status_text: 'Delivery exception',
      tracker_id: 'trk_fernhill',
      aggregator: 'ship24',
      source_message_id: 103,
      last_event_description: 'The carrier could not find the address',
      last_event_ts: now - 2 * HOUR,
    }),
    item({
      id: 4,
      merchant: 'Cobble Books',
      carrier: 'DHL',
      tracking_number: '3456789012',
      status: 'out_for_delivery',
      status_text: 'Out for delivery',
      tracker_id: 'trk_cobble',
      aggregator: 'ship24',
      source_message_id: 104,
      last_event_description: 'On the van for delivery today',
      last_event_ts: now - HOUR,
    }),
    item({ id: 5, tracking_number: 'LP00123456789CN', status: 'ordered', status_text: 'Ordered' }),
    item({
      id: 6,
      merchant: 'Pine Street Tea',
      carrier: 'Royal Mail',
      tracking_number: 'RN123456785GB',
      status: 'delivered',
      status_text: 'Delivered',
      tracker_id: 'trk_pine',
      aggregator: 'ship24',
      source_message_id: 105,
      last_event_description: 'Left at the front door',
      last_event_ts: now - 26 * HOUR,
    }),
  ];
  if (scenario.list !== 'many') return base;
  return [
    ...base,
    ...Array.from({ length: 130 }, (_, index) =>
      item({
        id: 100 + index,
        merchant: `Corner Shop ${index + 1}`,
        tracking_number: `CS${String(index + 1).padStart(8, '0')}`,
        status: 'shipped',
        source_message_id: 200 + index,
      }),
    ),
  ];
}

function error(code: string, message: string, status: number): Response {
  return HttpResponse.json({ error: { code, message } }, { status });
}

export function packageHandlers(read: () => { packages?: PackagesScenario; apps?: AppsScenario }) {
  let store: Package[] = [];
  let used = 7;
  let seededFrom: string | undefined;
  let nextId = 500;
  let nextEffect = 9000;
  const undo = new Map<number, { id: number; tracker: string }>();

  const scenario = (): PackagesScenario => read().packages ?? {};
  const current = (): Package[] => {
    const key = JSON.stringify(scenario());
    if (key !== seededFrom) {
      seededFrom = key;
      store = seedPackages(scenario());
      used = scenario().quota === 'spent' ? LIMIT : 7;
      undo.clear();
    }
    return store;
  };
  const off = (): boolean => {
    const apps = read().apps ?? {};
    return apps.off?.includes('packages') === true || apps.on?.includes('packages') !== true;
  };
  const gate = async (): Promise<Response | undefined> => {
    const ms = scenario().delay;
    if (ms) await delay(ms);
    if (off()) return error('not_found', 'Not found.', 404);
    return undefined;
  };
  const write = (): Response | undefined => {
    const refuse = scenario().refuse;
    if (refuse) return error(refuse.code, refuse.message, refuse.status ?? 409);
    if (scenario().unknown) {
      return error('effect_unknown', 'Ship24 did not confirm the change before the connection closed.', 409);
    }
    return undefined;
  };
  const find = (id: string | readonly string[] | undefined): Package | undefined =>
    current().find((entry) => String(entry.id) === String(id));

  return [
    http.get(`*${API_BASE}/packages`, async ({ request }) => {
      const stop = await gate();
      if (stop) return stop;
      if (scenario().list === 'error') return error('unavailable', 'The package list could not be read.', 503);
      const url = new URL(request.url);
      const limit = Number(url.searchParams.get('limit') ?? '50');
      const start = Number(url.searchParams.get('cursor') ?? '0');
      const all = current().filter((entry) => entry.dismissed === 0);
      const items = all.slice(start, start + limit);
      const next = start + limit < all.length ? String(start + limit) : null;
      return HttpResponse.json({
        data: {
          items,
          next_cursor: next,
          quota: { used, limit: LIMIT, remaining: Math.max(0, LIMIT - used) },
          ship24_connected: scenario().ship24 !== 'missing',
        },
        revision: '1',
      });
    }),
    http.post(`*${API_BASE}/packages`, async ({ request }) => {
      const stop = (await gate()) ?? write();
      if (stop) return stop;
      const body = (await request.json()) as PackageAdd;
      const number = (body.tracking_number ?? '').trim();
      if (number === '') return error('invalid', 'tracking_number is required.', 422);
      if (current().some((entry) => entry.tracking_number === number && entry.dismissed === 0)) {
        return error('conflict', `Package ${number} is already listed.`, 409);
      }
      const added = item({
        id: nextId++,
        tracking_number: number,
        carrier: body.carrier ?? '',
        merchant: body.merchant ?? '',
        status: 'ordered',
        status_text: 'Ordered',
        first_seen: now,
        updated_ts: now,
      });
      current().unshift(added);
      return HttpResponse.json({ data: { package_id: added.id } });
    }),
    http.post(`*${API_BASE}/packages/poll`, async () => {
      const stop = await gate();
      if (stop) return stop;
      if (scenario().busy) {
        return HttpResponse.json(
          { error: { code: 'unavailable', message: 'A package poll is already running.', retry_after: 2 } },
          { status: 503, headers: { 'Retry-After': '2' } },
        );
      }
      const refuse = scenario().refuse;
      if (refuse) return error(refuse.code, refuse.message, refuse.status ?? 409);
      if (scenario().ship24 === 'missing')
        return error('not_configured', 'The Ship24 connection is not configured.', 503);
      const checked = current().filter((entry) => entry.tracker_id !== '' && entry.status !== 'delivered').length;
      return HttpResponse.json({ data: { poll: { checked, events: 1, notified: 0, updated: 1 } } });
    }),
    http.post(`*${API_BASE}/packages/:id/track`, async ({ params }) => {
      const stop = (await gate()) ?? write();
      if (stop) return stop;
      const target = find(params.id);
      if (!target) return error('invalid', `no package with id ${String(params.id)}`, 422);
      if (scenario().ship24 === 'missing')
        return error('not_configured', 'The Ship24 connection is not configured.', 503);
      if (used >= LIMIT) {
        return error('refused', `The Ship24 monthly quota of ${LIMIT} lookups is used up.`, 409);
      }
      used += 1;
      target.tracker_id = `trk_${target.id}`;
      target.aggregator = 'ship24';
      target.status = target.status === 'ordered' ? 'shipped' : target.status;
      target.status_text = 'Ship24 is following this package';
      return HttpResponse.json({
        data: {
          tracking: {
            events: [],
            package_id: target.id,
            status: target.status,
            status_text: target.status_text,
            tracker_id: target.tracker_id,
          },
        },
        effect_ids: [nextEffect++],
        undoable: false,
      });
    }),
    http.post(`*${API_BASE}/packages/:id/untrack`, async ({ params }) => {
      const stop = (await gate()) ?? write();
      if (stop) return stop;
      const target = find(params.id);
      if (!target) return error('not_found', `no package with id ${String(params.id)}`, 404);
      if (target.tracker_id === '') return error('conflict', 'This package is not tracked.', 409);
      const effect = nextEffect++;
      undo.set(effect, { id: target.id, tracker: target.tracker_id });
      const tracker = target.tracker_id;
      target.tracker_id = '';
      return HttpResponse.json({
        data: { package_id: target.id, tracker_id: tracker },
        effect_ids: [effect],
        undoable: true,
      });
    }),
    http.post(`*${API_BASE}/packages/:id/dismiss`, async ({ params }) => {
      const stop = (await gate()) ?? write();
      if (stop) return stop;
      const target = find(params.id);
      if (target === undefined || target.dismissed > 0) return error('not_found', 'Not found.', 404);
      target.dismissed = 1;
      return HttpResponse.json({ data: {} });
    }),
    http.post(`*${API_BASE}/effects/:id/undo`, async ({ params }) => {
      const entry = undo.get(Number(params.id));
      if (!entry) return undefined;
      const target = find(String(entry.id));
      if (target) target.tracker_id = entry.tracker;
      undo.delete(Number(params.id));
      return HttpResponse.json({ undone: true });
    }),
  ];
}
