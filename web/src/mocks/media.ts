import { delay, HttpResponse, http } from 'msw';
import type {
  MediaAddInput,
  MediaCatalogue,
  MediaChannel,
  MediaChannelInput,
  MediaFilingPreview,
  MediaLibraryEntry,
  MediaLibraryFile,
  MediaQueueItem,
  MediaSelectInput,
  MediaShow,
  MediaTorrent,
  TorrentControlInput,
  TorrentFile,
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
  tmdb?: 'off' | 'error';
  releases?: 'error';
  poll?: 'failed' | 'partial';
  art?: { none?: string[]; down?: string[] };
}

interface Store {
  torrents: MediaTorrent[];
  files: Record<string, TorrentFile[]>;
  channels: MediaChannel[];
  polled: boolean;
  queue: MediaQueueItem[];
}

const UPLOAD_BYTES = 786_432;

function file(index: number, name: string, size: number, progress: number, priority = 1): TorrentFile {
  return { index, name, size, progress, priority };
}

function seedFiles(scenario: MediaScenario): Record<string, TorrentFile[]> {
  const files: Record<string, TorrentFile[]> = {
    a1: [
      file(0, 'Harbour.Lights.S02E07.1080p.WEB.mkv', 2_350_000_000, 0.42),
      file(1, 'Harbour.Lights.S02E07.1080p.WEB.en.srt', 48_000, 1),
      file(2, 'Sample/harbour.lights.s02e07.sample.mkv', 52_000_000, 0, 0),
    ],
    b2: [
      file(0, 'The.Quiet.Orchard.2024.1080p.mkv', 8_000_000_000, 0.18),
      file(1, 'The.Quiet.Orchard.2024.1080p.nfo', 4_000, 1),
    ],
    c3: [file(0, 'Lantern.Street.S01E03.720p.mkv', 950_000_000, 1)],
    d4: [
      file(0, 'Paper.Boats.1998.Remastered.mkv', 4_200_000_000, 0.63),
      file(1, 'Extras/Paper.Boats.Commentary.mkv', 100_000_000, 0.63),
    ],
    e5: [
      file(0, 'Garden.Club.Lectures.Vol2.Part1.mp4', 400_000_000, 1),
      file(1, 'Garden.Club.Lectures.Vol2.Part2.mp4', 410_000_000, 1),
    ],
  };
  if (scenario.torrents === 'empty' || scenario.torrents === 'unavailable') return {};
  return files;
}

function magnetHash(uri: string): string | undefined {
  const match = /[?&]xt=urn:btih:([0-9a-f]{40}|[a-z2-7]{32})(?:&|$)/i.exec(uri);
  return match?.[1]?.toLowerCase();
}

function magnetName(uri: string): string | undefined {
  const raw = new URLSearchParams(uri.slice('magnet:?'.length)).get('dn');
  return raw?.trim() || undefined;
}

async function torrentHash(data: ArrayBuffer): Promise<string | undefined> {
  if (new Uint8Array(data)[0] !== 0x64) return undefined;
  const sum = new Uint8Array(await crypto.subtle.digest('SHA-1', data));
  const digits = '0123456789abcdef';
  return Array.from(sum, (byte) => `${digits[byte >> 4]}${digits[byte & 15]}`).join('');
}

function ownedByAtrium(torrent: MediaTorrent): boolean {
  return torrent.tags
    .split(',')
    .map((tag) => tag.trim())
    .some((tag) => tag === 'atrium' || tag === 'media');
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

const shows: MediaShow[] = [
  { id: 4101, name: 'Harbour Lights', year: '2023' },
  { id: 4102, name: 'Harbour Lights Revisited', year: '2026' },
  { id: 4201, name: 'Lantern Street', year: '2025' },
  { id: 4301, name: 'Moss and Mortar', year: '2022' },
  { id: 4302, name: 'Mortar Board', year: '' },
];

const imdbOfShow: Record<number, string> = { 4101: 'tt0000101', 4201: 'tt0000201', 4301: 'tt0000301' };

const releasesOf: Record<string, string[]> = {
  tt0000101: [
    'Harbour.Lights.S02E08.1080p.WEB.h264',
    'Harbour.Lights.S02E08.720p.HDTV.x264',
    'Harbour.Lights.S02E07.2160p.WEB.h265',
    'Harbour.Lights.S02E07.1080p.WEB.h264',
  ],
  tt0000301: [
    'Moss.and.Mortar.S01E02.1080p.WEB.h264',
    'Moss.and.Mortar.S01E02.720p.WEB.h264',
    'Moss.and.Mortar.S01E01.1080p.WEB.h264',
  ],
};

const artShades: Record<string, string> = { movie: '#5f6f3a', show: '#8a5a2b' };

function artOf(kind: string, id: string): string {
  const shade = artShades[kind] ?? '#6b6b4a';
  return `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="300" viewBox="0 0 200 300"><rect width="200" height="300" fill="${shade}"/><circle cx="100" cy="120" r="${40 + (id.charCodeAt(1) % 5) * 8}" fill="#f2e6c9" opacity="0.7"/></svg>`;
}

function imdbDigits(value: string): string | undefined {
  const digits = /^(?:tt)?(\d{1,10})$/.exec(value.trim())?.[1];
  return digits ? `tt${digits.padStart(7, '0')}` : undefined;
}

function error(code: string, message: string, status: number): Response {
  return HttpResponse.json({ error: { code, message } }, { status });
}

export function mediaHandlers(read: () => { media?: MediaScenario; apps?: AppsScenario }) {
  let store: Store = { torrents: [], files: {}, channels: [], queue: [], polled: false };
  let seededFrom: string | undefined;
  let nextChannel = 10;

  const current = (): Store => {
    const scenario = read().media ?? {};
    const key = JSON.stringify(scenario);
    if (key !== seededFrom) {
      seededFrom = key;
      store = {
        torrents: seedTorrents(scenario),
        files: seedFiles(scenario),
        channels: seedChannels(scenario),
        queue: seedQueue(scenario),
        polled: false,
      };
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
    http.post(`*${API_BASE}/media/torrents`, async ({ request }) => {
      await wait();
      if (off('torrents')) return error('not_found', 'This app is off.', 404);
      const refused = refusal();
      if (refused) return refused;
      let magnet: string | undefined;
      let upload: File | undefined;
      if ((request.headers.get('Content-Type') ?? '').startsWith('multipart/form-data')) {
        const form = await request.formData();
        const names = [...form.keys()];
        if (names.some((name) => name !== 'magnet' && name !== 'torrent') || new Set(names).size !== names.length) {
          return error('invalid', 'Give one magnet or torrent file.', 422);
        }
        const part = form.get('torrent');
        if (part !== null && typeof part === 'string') return error('invalid', 'torrent must be a file.', 422);
        upload = part ?? undefined;
        const text = form.get('magnet');
        if (text !== null && typeof text !== 'string') return error('invalid', 'magnet must be a text field.', 422);
        magnet = text ?? undefined;
        if (upload && upload.size > UPLOAD_BYTES) return error('invalid', 'The torrent input is too large.', 422);
      } else {
        const body = (await request.json()) as MediaAddInput;
        magnet = body.magnet;
      }
      if (magnet !== undefined && magnet !== '' && upload) {
        return error('invalid', 'Give a magnet link or a .torrent file, not both.', 422);
      }
      const store = current();
      let added: MediaTorrent;
      if (upload) {
        const hash = await torrentHash(await upload.arrayBuffer());
        if (!hash) return error('invalid', 'not a torrent file', 422);
        const name = upload.name.replace(/\.torrent$/i, '') || 'upload';
        added = { hash, name, state: 'downloading', tags: 'media', progress: 0, size: upload.size * 1000 };
        store.files[hash] = [
          file(0, `${name}/${name}.mkv`, upload.size * 900, 0),
          file(1, `${name}/${name}.nfo`, 2_000, 0),
        ];
      } else {
        const uri = (magnet ?? '').trim();
        if (uri === '') return error('invalid', 'Paste a magnet link or choose a .torrent file.', 422);
        if (!uri.startsWith('magnet:?')) return error('invalid', 'That is not a magnet link.', 422);
        const hash = magnetHash(uri);
        if (!hash) return error('invalid', 'The magnet link has no v1 info hash (xt=urn:btih:...).', 422);
        added = { hash, name: magnetName(uri) ?? hash, state: 'metaDL', tags: 'media', progress: 0, size: null };
        store.files[hash] = [];
      }
      if (!store.torrents.some((item) => item.hash === added.hash)) store.torrents.push(added);
      return HttpResponse.json({ hash: added.hash });
    }),
    http.get(`*${API_BASE}/media/torrents/:hash/files`, async ({ params, request }) => {
      await wait();
      if (off('torrents')) return error('not_found', 'This app is off.', 404);
      const hash = String(params.hash);
      const store = current();
      if (!store.torrents.some((item) => item.hash === hash)) return error('not_found', 'unknown torrent', 404);
      const url = new URL(request.url);
      const limit = Number(url.searchParams.get('limit') ?? '50');
      const after = Number(url.searchParams.get('cursor') ?? '-1');
      const all = (store.files[hash] ?? []).filter((item) => item.index > after);
      const items = all.slice(0, limit);
      const last = items.at(-1);
      const next = all.length > limit && last ? String(last.index) : undefined;
      return HttpResponse.json({ items, ...(next ? { next } : {}) });
    }),
    http.put(`*${API_BASE}/media/torrents/:hash/files`, async ({ params, request }) => {
      await wait();
      if (off('torrents')) return error('not_found', 'This app is off.', 404);
      const refused = refusal();
      if (refused) return refused;
      const hash = String(params.hash);
      const store = current();
      const torrent = store.torrents.find((item) => item.hash === hash);
      if (!torrent || !ownedByAtrium(torrent)) return error('not_found', 'unknown torrent', 404);
      const body = (await request.json()) as MediaSelectInput;
      const target = store.files[hash]?.find((item) => item.index === body.index);
      if (target) target.priority = body.wanted ? 1 : 0;
      return HttpResponse.json({ hash });
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
    http.get(`*${API_BASE}/media/shows`, async ({ request }) => {
      await wait();
      const blocked = gone();
      if (blocked) return blocked;
      if (scenario().tmdb === 'off') return error('not_configured', 'media_tmdb_token is not set', 503);
      if (scenario().tmdb === 'error') return error('unavailable', 'TMDB did not answer.', 503);
      const url = new URL(request.url);
      const q = (url.searchParams.get('q') ?? '').trim().toLowerCase();
      if (q === '') return error('invalid', 'Type a show name to search.', 422);
      const limit = Number(url.searchParams.get('limit') ?? '20');
      const start = Number(url.searchParams.get('cursor') ?? '0');
      const all = shows.filter((show) => show.name.toLowerCase().includes(q));
      const next = start + limit < all.length ? String(start + limit) : undefined;
      return HttpResponse.json({ items: all.slice(start, start + limit), ...(next ? { next } : {}) });
    }),
    http.get(`*${API_BASE}/media/shows/:id`, async ({ params }) => {
      await wait();
      const blocked = gone();
      if (blocked) return blocked;
      if (scenario().tmdb === 'off') return error('not_configured', 'media_tmdb_token is not set', 503);
      if (scenario().tmdb === 'error') return error('unavailable', 'TMDB did not answer.', 503);
      const imdb = imdbOfShow[Number(params.id)];
      return HttpResponse.json({ imdb: imdb ?? '', found: imdb !== undefined });
    }),
    http.get(`*${API_BASE}/media/channels/preview`, async ({ request }) => {
      await wait();
      const blocked = gone();
      if (blocked) return blocked;
      const url = new URL(request.url);
      const imdb = imdbDigits(url.searchParams.get('imdb') ?? '');
      if (!imdb) return error('invalid', 'Pick a show or enter an IMDb id first.', 422);
      if (scenario().releases === 'error') return error('unavailable', 'The release feed did not answer.', 503);
      const pattern = (url.searchParams.get('pattern') ?? '').trim();
      let match: RegExp | undefined;
      if (pattern !== '') {
        try {
          match = new RegExp(pattern, url.searchParams.get('case_sensitive') === 'true' ? '' : 'i');
        } catch (failure) {
          return error('invalid', `pattern "${pattern}": ${(failure as Error).message}`, 422);
        }
      }
      const items = (releasesOf[imdb] ?? []).map((title) => ({ title, match: match?.test(title) ?? false }));
      return HttpResponse.json({ items, matched: items.filter((item) => item.match).length });
    }),
    http.post(`*${API_BASE}/media/channels/poll`, async () => {
      await wait();
      const blocked = gone() ?? refusal();
      if (blocked) return blocked;
      const store = current();
      if (store.channels.length === 0) return error('not_configured', 'no show is followed', 503);
      if (scenario().poll === 'failed') return error('unavailable', 'Channel poll failed.', 503);
      const fresh = store.polled ? [] : ['Harbour.Lights.S02E08.1080p.WEB.h264'];
      store.polled = true;
      for (const name of fresh) {
        const hash = `poll${store.torrents.length}`;
        store.torrents.push({ hash, name, state: 'metaDL', tags: 'media', progress: 0, size: null });
        store.files[hash] = [];
      }
      if (scenario().poll === 'partial')
        return error('effect_partial', 'Channel poll stopped after adding torrents.', 409);
      return HttpResponse.json({ added: fresh.length, errors: 0 });
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
    http.get(`*${API_BASE}/media/art/:kind/:id`, async ({ params }) => {
      await wait();
      const blocked = gone();
      if (blocked) return blocked;
      const kind = String(params.kind);
      const id = String(params.id);
      if (!entries.some((entry) => entry.kind === kind && entry.id === id))
        return error('not_found', 'Not found.', 404);
      const art = scenario().art;
      if (art?.down?.includes(id)) return error('unavailable', 'The library server did not answer.', 503);
      if (art?.none?.includes(id)) return error('not_found', 'Not found.', 404);
      return new HttpResponse(artOf(kind, id), { headers: { 'Content-Type': 'image/svg+xml' } });
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
