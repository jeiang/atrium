import { createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  atriumFetch,
  getMediaAddUrl,
  type MediaTorrent,
  mediaAdd,
  type mediaAddResponse,
  mediaDownloads,
  mediaSelectFiles,
  mediaTorrentFiles,
  ok,
  type TorrentFile,
  torrentsControl,
} from '../../contract';
import { outcomeUnknown, refusedOffline } from '../../shared/device.svelte';
import type { Tone } from '../../shared/ui/Badge.svelte';
import { toast } from '../../shared/ui/toast.svelte';

export const TORRENT_TOPICS = ['effects'] as const;
const PAGE = 100;
const MAX_PAGES = 20;

export interface TorrentList {
  items: MediaTorrent[];
  unavailable: boolean;
}

export async function readTorrents(signal: AbortSignal): Promise<TorrentList> {
  const items: MediaTorrent[] = [];
  let unavailable = false;
  let cursor: string | undefined;
  for (let page = 0; page < MAX_PAGES; page++) {
    const data = ok(await mediaDownloads({ limit: PAGE, ...(cursor ? { cursor } : {}) }, { signal }));
    items.push(...data.items);
    unavailable ||= data.unavailable;
    if (!data.next) break;
    cursor = data.next;
  }
  return { items, unavailable };
}

export function useTorrents(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['torrents'],
    queryFn: ({ signal }: { signal: AbortSignal }) => readTorrents(signal),
    refetchInterval: 15_000,
    enabled: enabled(),
    meta: { topics: TORRENT_TOPICS },
  }));
}

export type Phase = 'downloading' | 'stalled' | 'queued' | 'checking' | 'paused' | 'seeding' | 'done' | 'errored';

const phases: Record<string, Phase> = {
  downloading: 'downloading',
  forcedDL: 'downloading',
  metaDL: 'downloading',
  forcedMetaDL: 'downloading',
  stalledDL: 'stalled',
  queuedDL: 'queued',
  checkingDL: 'checking',
  checkingUP: 'checking',
  checkingResumeData: 'checking',
  allocating: 'checking',
  moving: 'checking',
  pausedDL: 'paused',
  stoppedDL: 'paused',
  uploading: 'seeding',
  forcedUP: 'seeding',
  stalledUP: 'seeding',
  queuedUP: 'seeding',
  pausedUP: 'done',
  stoppedUP: 'done',
  error: 'errored',
  missingFiles: 'errored',
};

export function phaseOf(torrent: MediaTorrent): Phase {
  return phases[torrent.state] ?? (torrent.progress >= 1 ? 'seeding' : 'downloading');
}

export const phaseWords: Record<Phase, { label: string; tone: Tone }> = {
  downloading: { label: 'Downloading', tone: 'accent' },
  stalled: { label: 'Stalled', tone: 'warn' },
  queued: { label: 'Queued', tone: 'neutral' },
  checking: { label: 'Checking', tone: 'neutral' },
  paused: { label: 'Paused', tone: 'neutral' },
  seeding: { label: 'Seeding', tone: 'ok' },
  done: { label: 'Done', tone: 'ok' },
  errored: { label: 'Error', tone: 'error' },
};

export function stopped(phase: Phase): boolean {
  return phase === 'paused' || phase === 'done' || phase === 'errored';
}

export function tagsOf(torrent: MediaTorrent): string[] {
  return torrent.tags
    .split(',')
    .map((tag) => tag.trim())
    .filter((tag) => tag !== '');
}

export function addedByAtrium(torrent: MediaTorrent): boolean {
  const tags = tagsOf(torrent);
  return tags.includes('atrium') || tags.includes('media');
}

export function bytes(size: number | null | undefined): string {
  if (size === null || size === undefined) return 'Size unknown';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = size;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit += 1;
  }
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
}

export function percent(progress: number): string {
  return `${Math.floor(Math.min(1, Math.max(0, progress)) * 100)}%`;
}

export type Action = 'pause' | 'resume' | 'remove';

export async function handleControl(hash: string, action: Action, deleteFiles = false): Promise<void> {
  ok(await torrentsControl(hash, action === 'remove' ? { action, delete_files: deleteFiles } : { action }));
}

export type AddSource = { magnet: string } | { file: File };

export async function handleAdd(source: AddSource): Promise<string> {
  if ('magnet' in source) return ok(await mediaAdd({ magnet: source.magnet })).hash;
  const form = new FormData();
  form.append('torrent', source.file, source.file.name);
  return ok(await atriumFetch<mediaAddResponse>(getMediaAddUrl(), { method: 'POST', body: form })).hash;
}

export async function readFiles(hash: string, signal: AbortSignal): Promise<TorrentFile[]> {
  const items: TorrentFile[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < MAX_PAGES; page++) {
    const data = ok(await mediaTorrentFiles(hash, { limit: PAGE, ...(cursor ? { cursor } : {}) }, { signal }));
    items.push(...data.items);
    if (!data.next) break;
    cursor = data.next;
  }
  return items;
}

export function useFiles(hash: () => string | undefined) {
  return createQuery(() => ({
    queryKey: ['torrents', hash(), 'files'],
    queryFn: ({ signal }: { signal: AbortSignal }) => readFiles(hash() ?? '', signal),
    enabled: hash() !== undefined,
    meta: { topics: TORRENT_TOPICS },
  }));
}

export function wanted(file: TorrentFile): boolean {
  return file.priority > 0;
}

export async function handleSelect(hash: string, index: number, want: boolean): Promise<void> {
  ok(await mediaSelectFiles(hash, { index, wanted: want }));
}

export function addProblem(error: unknown): Problem {
  if (refusedOffline(error) || outcomeUnknown(error)) return problemOf(error);
  if (error instanceof ApiError && error.status >= 400 && error.status < 500) {
    return { tone: 'error', title: 'atrium did not add it', text: `${error.message} Nothing was added.` };
  }
  return problemOf(error);
}

export interface Problem {
  tone: 'warn' | 'error';
  title: string;
  text: string;
}

export function problemOf(error: unknown): Problem {
  if (refusedOffline(error) && error instanceof Error) {
    return { tone: 'warn', title: 'Cannot reach atrium', text: error.message };
  }
  if (outcomeUnknown(error)) {
    return {
      tone: 'error',
      title: 'The outcome is unknown',
      text: `${error.message} atrium reloaded the list and did not try again.`,
    };
  }
  if (error instanceof ApiError && error.status === 404) {
    return { tone: 'warn', title: 'This torrent is gone', text: `${error.message} atrium reloaded the list.` };
  }
  if (error instanceof ApiError && error.status === 503) {
    return { tone: 'error', title: 'qBittorrent did not answer', text: `${error.message} Nothing was changed.` };
  }
  if (error instanceof ApiError) {
    return { tone: 'error', title: 'atrium refused this', text: `${error.message} Nothing was changed.` };
  }
  return {
    tone: 'error',
    title: 'The outcome is unknown',
    text: 'The connection failed before atrium answered. atrium reloaded the list and did not try again.',
  };
}

export function refreshTorrents(client: QueryClient): void {
  void client.invalidateQueries({ queryKey: ['torrents'] });
}

const opposite: Record<'pause' | 'resume', { action: 'pause' | 'resume'; done: string }> = {
  pause: { action: 'resume', done: 'Resumed' },
  resume: { action: 'pause', done: 'Paused' },
};

export function controlToast(torrent: MediaTorrent, action: 'pause' | 'resume', client: QueryClient): void {
  const undo = opposite[action];
  const done = action === 'pause' ? 'Paused' : 'Resumed';
  toast(`${done} ${torrent.name}.`, {
    action: {
      label: 'Undo',
      run: () =>
        void handleControl(torrent.hash, undo.action)
          .then(() => toast(`${undo.done} ${torrent.name} again.`))
          .catch((error: unknown) => {
            const problem = problemOf(error);
            toast(`${problem.title}. ${problem.text}`, { tone: 'error' });
          })
          .finally(() => refreshTorrents(client)),
    },
  });
}
