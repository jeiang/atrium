import { delay, HttpResponse, http } from 'msw';
import type { Group, Hit, Result } from '../contract';
import { API_BASE } from '../contract';

export interface SearchScenario {
  fail?: boolean;
  slow?: boolean;
  partial?: boolean;
}

const ORDER: Hit['type'][] = ['thread', 'message', 'record', 'task', 'file'];
const WHEN = 1_795_600_000;
const log = globalThis as typeof globalThis & { atriumSearchRequests?: string[] };

function hit(type: Hit['type'], id: number, title: string, detail: string, snippet: string, path: string): Hit {
  return { type, id, title, detail, snippet, path, matches: [], when: WHEN - id * 60, private: false };
}

const HITS: Hit[] = [
  hit(
    'thread',
    401,
    'Garden plans with Ilse',
    '',
    'We talked through the garden beds and the compost.',
    '/threads/401',
  ),
  hit(
    'message',
    501,
    'Re: Shared garden plans',
    'Ilse de Wit',
    'Thanks for the garden plans, see you Thursday.',
    '/mail/messages/501',
  ),
  hit('record', 21, 'Ilse de Wit', 'Page', 'Ilse runs the shared garden on Mill Lane.', '/knowledge/21'),
  hit(
    'record',
    22,
    'Shared garden project',
    'Page',
    'Beds, compost and the seed order for next spring.',
    '/knowledge/22',
  ),
  hit('task', 7, 'Order raised bed timber', 'Garden', 'Order timber for the garden beds before December.', '/tasks/7'),
  hit('file', 601, 'garden-layout.pdf', 'Garden plans with Ilse', 'garden-layout.pdf', '/threads/401/files/601'),
  hit('file', 602, 'garden-seeds.csv', 'Garden space', 'garden-seeds.csv', '/knowledge/spaces/3/files/602'),
  {
    ...hit(
      'record',
      24,
      'Water the tomatoes in the morning',
      'Lesson',
      'Garden lesson about watering.',
      '/knowledge/24',
    ),
    private: true,
  },
  ...Array.from({ length: 24 }, (_, n) =>
    hit(
      'record',
      100 + n,
      `Seed catalogue note ${n + 1}`,
      'Page',
      `Garden seed packet ${n + 1}.`,
      `/knowledge/${100 + n}`,
    ),
  ),
];

function scenario(): SearchScenario {
  try {
    const parsed: { search?: SearchScenario } = JSON.parse(localStorage.getItem('atrium.mock') ?? '{}');
    return parsed.search ?? {};
  } catch {
    return {};
  }
}

function marked(item: Hit, q: string): Hit {
  const at = item.snippet.toLowerCase().indexOf(q);
  return at < 0 ? item : { ...item, matches: [{ start: at, end: at + q.length }] };
}

export const searchHandlers = [
  http.get(`*${API_BASE}/search/all`, async ({ request }) => {
    const state = scenario();
    const url = new URL(request.url);
    log.atriumSearchRequests = [...(log.atriumSearchRequests ?? []), `${url.pathname}${url.search}`];
    if (state.slow) await delay('infinite');
    if (state.fail)
      return HttpResponse.json({ error: { code: 'internal', message: 'Search did not answer.' } }, { status: 500 });
    const q = (url.searchParams.get('q') ?? '').trim().toLowerCase();
    if (!q) {
      return HttpResponse.json(
        { error: { code: 'invalid', message: 'Give a search of 1 to 200 characters.' } },
        { status: 400 },
      );
    }
    const types = (url.searchParams.get('types') ?? '').split(',').filter(Boolean) as Hit['type'][];
    const limit = Number(url.searchParams.get('limit') ?? '5');
    const offset = Number(url.searchParams.get('cursor') ?? '0');
    const wanted = types.length > 0 ? types : ORDER;
    const matching = HITS.filter(
      (item) =>
        wanted.includes(item.type) &&
        `${item.title} ${item.snippet} ${item.detail}`.toLowerCase().includes(q) &&
        !(state.partial && item.type === 'message'),
    ).map((item) => marked(item, q));
    const groups: Group[] = ORDER.filter((type) => wanted.includes(type)).flatMap((type) => {
      const all = matching.filter((item) => item.type === type);
      const items = all.slice(offset, offset + limit);
      return items.length > 0 ? [{ type, items, more: offset + limit < all.length }] : [];
    });
    const single = wanted.length === 1 && groups.length === 1 && groups[0]?.more;
    const result: Result = {
      query: q,
      top: groups.slice(0, 3).flatMap((group) => group.items.slice(0, 1)),
      groups,
      next_cursor: single ? String(offset + limit) : null,
      partial: state.partial ? [{ type: 'message', reason: 'timeout' }] : [],
    };
    return HttpResponse.json({ revision: 1, data: result });
  }),
];
