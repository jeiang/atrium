import { delay, HttpResponse, http } from 'msw';
import type {
  MediaCatalogue,
  MediaChannel,
  MediaChannelInput,
  MediaFilingPreview,
  MediaLibraryEntry,
  MediaLibraryFile,
  MediaQueueItem,
  MediaTorrent,
  TorrentControlInput,
} from '../contract';
import { API_BASE } from '../contract';
import type { AppsScenario } from './apps';

export interface MediaScenario {
  torrents?: 'empty' | 'unavailable' | 'error' | 'many';
  following?: 'empty';
  queue?: 'empty';
  catalogue?: 'empty' | 'stale' | 'error';
  refuse?: { code: string; message: string; status?: number };
  delay?: number;
}

interface Store {
  torrents: MediaTorrent[];
  channels: MediaChannel[];
  queue: MediaQueueItem[];
}

const observed = '2026-11-14T09:30:00Z';

function seedTorrents(scenario: MediaScenario): MediaTorrent[] {
  if (scenario.torrents === 'empty' || scenario.torrents === 'unavailable') return [];
  const base: MediaTorrent[] = [
    {
      hash: 'a1',
      name: 'Harbour.Lights.S02E07.1080p.WEB',
      state: 'downloading',
      tags: 'atrium',
      progress: 0.42,
      size: 2_400_000_000,
    },
    {
      hash: 'b2',
      name: 'The.Quiet.Orchard.2024.1080p',
      state: 'pausedDL',
      tags: 'atrium',
      progress: 0.18,
      size: 8_100_000_000,
    },
    {
      hash: 'c3',
      name: 'Lantern.Street.S01E03.720p',
      state: 'uploading',
      tags: 'media',
      progress: 1,
      size: 950_000_000,
    },
    {
      hash: 'd4',
      name: 'Paper.Boats.1998.Remastered',
      state: 'error',
      tags: 'atrium',
      progress: 0.63,
      size: 4_300_000_000,
    },
    { hash: 'e5', name: 'Garden.Club.Lectures.Vol2', state: 'stalledUP', tags: '', progress: 1, size: null },
  ];
  if (scenario.torrents !== 'many') return base;
  return [
    ...base,
    ...Array.from({ length: 140 }, (_, index) => ({
      hash: `f${index}`,
      name: `Cedar.Workshop.E${String(index + 1).padStart(3, '0')}`,
      state: 'stalledUP',
      tags: 'atrium',
      progress: 1,
      size: 300_000_000,
    })),
  ];
}

function seedChannels(scenario: MediaScenario): MediaChannel[] {
  if (scenario.following === 'empty') return [];
  return [
    {
      id: 1,
      name: 'Harbour Lights',
      imdb: 'tt0000101',
      pattern: '',
      case_sensitive: false,
      primed: true,
      created: 1_760_000_000,
    },
    {
      id: 2,
      name: 'Lantern Street',
      imdb: '',
      pattern: 'Lantern.Street',
      case_sensitive: false,
      primed: false,
      created: 1_761_000_000,
    },
  ];
}

function seedQueue(scenario: MediaScenario): MediaQueueItem[] {
  if (scenario.queue === 'empty') return [];
  return [
    { id: 'c3', name: 'Lantern.Street.S01E03.720p', torrent: true },
    { id: 'q9', name: 'Paper.Boats.Extras', torrent: false, error: 'No title matched this folder.' },
  ];
}

const entries: MediaLibraryEntry[] = [
  { id: 'm1', kind: 'movie', title: 'The Quiet Orchard', year: '2024' },
  { id: 's1', kind: 'show', title: 'Harbour Lights', year: '2023' },
  { id: 'm2', kind: 'movie', title: 'Paper Boats', year: '1998' },
  { id: 's2', kind: 'show', title: 'Lantern Street', year: '2025' },
  { id: 'm3', kind: 'movie', title: 'A Winter Greenhouse', year: '2021' },
];

const files: Record<string, MediaLibraryFile[]> = {
  m1: [{ id: 'f-m1', name: 'The Quiet Orchard (2024).mkv', size: 7_900_000_000 }],
  s1: [
    { id: 'f-s1-1', name: 'Harbour Lights S01E01.mkv', size: 1_200_000_000, season: 1, episode: 1 },
    { id: 'f-s1-2', name: 'Harbour Lights S01E02.mkv', size: 1_150_000_000, season: 1, episode: 2 },
  ],
};

function catalogue(scenario: MediaScenario): MediaCatalogue {
  const stale = scenario.catalogue === 'stale';
  return { state: stale ? 'stale' : 'current', stale, observed_at: observed, generation: 7 };
}

function previewOf(item: MediaQueueItem): MediaFilingPreview {
  if (item.error) {
    return { id: item.id, kind: 'movie', title: item.name, year: '', files: [], ready: false, error: item.error };
  }
  return {
    id: item.id,
    kind: 'show',
    title: 'Lantern Street',
    year: '2025',
    ready: true,
    files: [
      {
        id: 'q-1',
        name: 'Lantern.Street.S01E03.720p.mkv',
        season: '1',
        episode: '3',
        destination: 'Shows/Lantern Street (2025)/Season 01',
      },
    ],
  };
}

function error(code: string, message: string, status: number): Response {
  return HttpResponse.json({ error: { code, message } }, { status });
}

export function mediaHandlers(read: () => { media?: MediaScenario; apps?: AppsScenario }) {
  let store: Store = { torrents: [], channels: [], queue: [] };
  let seededFrom: string | undefined;
  let nextChannel = 10;

  const current = (): Store => {
    const scenario = read().media ?? {};
    const key = JSON.stringify(scenario);
    if (key !== seededFrom) {
      seededFrom = key;
      store = { torrents: seedTorrents(scenario), channels: seedChannels(scenario), queue: seedQueue(scenario) };
    }
    return store;
  };
  const scenario = (): MediaScenario => read().media ?? {};
  const off = (id: string): boolean => read().apps?.off?.includes(id) === true;
  const wait = async (): Promise<void> => {
    const ms = scenario().delay;
    if (ms) await delay(ms);
  };
  const gone = (): Response | undefined =>
    off('torrents') || off('media') ? error('not_found', 'This app is off.', 404) : undefined;
  const refusal = (): Response | undefined => {
    const refuse = scenario().refuse;
    return refuse ? error(refuse.code, refuse.message, refuse.status ?? 409) : undefined;
  };

  return [
    http.get(`*${API_BASE}/media/torrents`, async ({ request }) => {
      await wait();
      if (off('torrents')) return error('not_found', 'This app is off.', 404);
      if (scenario().torrents === 'error') return error('unavailable', 'The download list could not be read.', 503);
      const url = new URL(request.url);
      const limit = Number(url.searchParams.get('limit') ?? '50');
      const start = Number(url.searchParams.get('cursor') ?? '0');
      const all = current().torrents;
      const items = all.slice(start, start + limit);
      const next = start + limit < all.length ? String(start + limit) : undefined;
      return HttpResponse.json({
        items,
        unavailable: scenario().torrents === 'unavailable',
        ...(next ? { next } : {}),
      });
    }),
    http.post(`*${API_BASE}/media/torrents/:hash/control`, async ({ params, request }) => {
      await wait();
      if (off('torrents')) return error('not_found', 'This app is off.', 404);
      const refused = refusal();
      if (refused) return refused;
      const body = (await request.json()) as TorrentControlInput;
      const torrents = current().torrents;
      const index = torrents.findIndex((item) => item.hash === params.hash);
      const torrent = torrents[index];
      if (!torrent) return error('not_found', 'qBittorrent has no such torrent.', 404);
      if (body.action === 'remove') torrents.splice(index, 1);
      else if (body.action === 'pause') torrent.state = torrent.progress >= 1 ? 'stoppedUP' : 'stoppedDL';
      else torrent.state = torrent.progress >= 1 ? 'uploading' : 'downloading';
      return HttpResponse.json({ hash: torrent.hash, action: body.action });
    }),
    http.get(`*${API_BASE}/media/channels`, async () => {
      await wait();
      return gone() ?? HttpResponse.json({ items: current().channels });
    }),
    http.post(`*${API_BASE}/media/channels`, async ({ request }) => {
      await wait();
      const blocked = gone() ?? refusal();
      if (blocked) return blocked;
      const body = (await request.json()) as MediaChannelInput;
      const channel: MediaChannel = {
        id: nextChannel++,
        name: body.name,
        imdb: body.imdb ?? '',
        pattern: body.pattern ?? '',
        case_sensitive: body.case_sensitive ?? false,
        primed: false,
        created: 1_763_000_000,
        ...(body.tmdb_id ? { tmdb_id: body.tmdb_id } : {}),
      };
      current().channels.push(channel);
      return HttpResponse.json({ channel, polled: false });
    }),
    http.delete(`*${API_BASE}/media/channels/:id`, async ({ params }) => {
      await wait();
      const blocked = gone() ?? refusal();
      if (blocked) return blocked;
      const channels = current().channels;
      const index = channels.findIndex((item) => String(item.id) === params.id);
      const channel = channels[index];
      if (!channel) return error('not_found', 'No such channel.', 404);
      channels.splice(index, 1);
      return HttpResponse.json({ channel, polled: false });
    }),
    http.get(`*${API_BASE}/media/queue`, async () => {
      await wait();
      return gone() ?? HttpResponse.json({ items: current().queue });
    }),
    http.get(`*${API_BASE}/media/filings/:hash`, async ({ params }) => {
      await wait();
      const blocked = gone();
      if (blocked) return blocked;
      const item = current().queue.find((entry) => entry.id === params.hash);
      return item ? HttpResponse.json(previewOf(item)) : error('not_found', 'Nothing waits under that name.', 404);
    }),
    http.post(`*${API_BASE}/media/filings/:hash/ignore`, async ({ params }) => {
      await wait();
      const blocked = gone() ?? refusal();
      if (blocked) return blocked;
      const queue = current().queue;
      const index = queue.findIndex((entry) => entry.id === params.hash);
      if (index < 0) return error('not_found', 'Nothing waits under that name.', 404);
      queue.splice(index, 1);
      return HttpResponse.json({ id: params.hash, placed: 0 });
    }),
    http.post(`*${API_BASE}/media/filings/:hash`, async ({ params }) => {
      await wait();
      const blocked = gone() ?? refusal();
      if (blocked) return blocked;
      const queue = current().queue;
      const index = queue.findIndex((entry) => entry.id === params.hash);
      if (index < 0) return error('not_found', 'Nothing waits under that name.', 404);
      queue.splice(index, 1);
      return HttpResponse.json({ id: params.hash, placed: 1 });
    }),
    http.get(`*${API_BASE}/media/library`, async ({ request }) => {
      await wait();
      const blocked = gone();
      if (blocked) return blocked;
      const state = scenario().catalogue;
      if (state === 'error') return error('unavailable', 'The library server did not answer.', 503);
      const q = (new URL(request.url).searchParams.get('q') ?? '').toLowerCase();
      const items =
        state === 'empty' ? [] : entries.filter((entry) => q === '' || entry.title.toLowerCase().includes(q));
      return HttpResponse.json({ items, catalogue: catalogue(scenario()) });
    }),
    http.get(`*${API_BASE}/media/library/:kind/:id`, async ({ params }) => {
      await wait();
      const blocked = gone();
      if (blocked) return blocked;
      const entry = entries.find((item) => item.kind === params.kind && item.id === params.id);
      if (!entry) return error('not_found', 'The library has no such title.', 404);
      return HttpResponse.json({ entry, items: files[entry.id] ?? [], catalogue: catalogue(scenario()) });
    }),
    http.get(`*${API_BASE}/media/files/:id/link`, async ({ params }) => {
      await wait();
      const blocked = gone();
      if (blocked) return blocked;
      return HttpResponse.json({
        public: `https://media.example.test/f/${params.id}?sig=demo`,
        ttl_seconds: 14_400,
        catalogue: catalogue(scenario()),
      });
    }),
  ];
}
