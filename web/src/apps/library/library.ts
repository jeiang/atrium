import { createInfiniteQuery, createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  type AppEntry,
  type MediaChannel,
  type MediaChannelInput,
  type MediaFilingPreview,
  mediaChannelAdd,
  mediaChannelRemove,
  mediaChannels,
  mediaFileItem,
  mediaFileLink,
  mediaFilingPreview,
  mediaIgnore,
  mediaLibrary,
  mediaLibraryDetail,
  mediaQueue,
  ok,
} from '../../contract';
import { refusedOffline } from '../../shared/device.svelte';
import { toast } from '../../shared/ui/toast.svelte';

export const LIBRARY_TOPICS = ['effects', 'jobs'] as const;
const PAGE = 48;
const MAX_PAGES = 20;

export type View = 'catalogue' | 'following' | 'intake';

export function blockedBy(app: AppEntry | undefined): string | undefined {
  if (!app?.enabled || app.unmet.length === 0) return undefined;
  const off = app.depends.filter((item) => !item.enabled).map((item) => item.name);
  return `${off.join(' and ')} is off${app.reason ? ` (${app.reason})` : ''}.`;
}

export function useCatalogue(q: () => string, enabled: () => boolean) {
  return createInfiniteQuery(() => {
    const search = q();
    return {
      queryKey: ['library', 'catalogue', search],
      queryFn: async ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) =>
        ok(await mediaLibrary({ q: search || undefined, limit: PAGE, cursor: pageParam || undefined }, { signal })),
      initialPageParam: '',
      getNextPageParam: (last: { next?: string }) => last.next || undefined,
      enabled: enabled(),
      meta: { topics: LIBRARY_TOPICS },
    };
  });
}

export function useItem(target: () => { kind: 'movie' | 'show'; id: string } | undefined, enabled: () => boolean) {
  return createQuery(() => {
    const item = target();
    return {
      queryKey: ['library', 'item', item?.kind, item?.id],
      queryFn: async ({ signal }: { signal: AbortSignal }) => {
        if (!item) throw new Error('No item');
        return ok(await mediaLibraryDetail(item.kind, item.id, { limit: 100 }, { signal }));
      },
      enabled: enabled() && item !== undefined,
      meta: { topics: LIBRARY_TOPICS },
    };
  });
}

async function allChannels(signal: AbortSignal): Promise<MediaChannel[]> {
  const items: MediaChannel[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < MAX_PAGES; page++) {
    const data = ok(await mediaChannels({ limit: 100, ...(cursor ? { cursor } : {}) }, { signal }));
    items.push(...data.items);
    if (!data.next) break;
    cursor = data.next;
  }
  return items;
}

export function useFollowing(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['library', 'following'],
    queryFn: ({ signal }: { signal: AbortSignal }) => allChannels(signal),
    enabled: enabled(),
    meta: { topics: LIBRARY_TOPICS },
  }));
}

export function useQueue(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['library', 'queue'],
    queryFn: async ({ signal }: { signal: AbortSignal }) => ok(await mediaQueue({ limit: 100 }, { signal })),
    enabled: enabled(),
    meta: { topics: LIBRARY_TOPICS },
  }));
}

export function usePreview(id: () => string | undefined) {
  return createQuery(() => {
    const hash = id();
    return {
      queryKey: ['library', 'preview', hash],
      queryFn: async ({ signal }: { signal: AbortSignal }) => {
        if (!hash) throw new Error('No item');
        return ok(await mediaFilingPreview(hash, undefined, { signal }));
      },
      enabled: hash !== undefined,
      meta: { topics: LIBRARY_TOPICS },
    };
  });
}

export async function handleFollow(input: MediaChannelInput): Promise<MediaChannel> {
  return ok(await mediaChannelAdd(input)).channel;
}

export async function handleUnfollow(channel: MediaChannel): Promise<void> {
  ok(await mediaChannelRemove(channel.id));
}

export async function handleFile(preview: MediaFilingPreview): Promise<number> {
  return ok(
    await mediaFileItem(preview.id, { action: 'file', kind: preview.kind, title: preview.title, year: preview.year }),
  ).placed;
}

export async function handleIgnore(id: string): Promise<void> {
  ok(await mediaIgnore(id));
}

export async function handleLink(fileId: string) {
  return ok(await mediaFileLink(fileId));
}

export function refreshLibrary(client: QueryClient): void {
  void client.invalidateQueries({ queryKey: ['library'] });
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
  if (error instanceof ApiError && error.status === 404) {
    return { tone: 'warn', title: 'This is gone', text: `${error.message} atrium reloaded the list.` };
  }
  if (error instanceof ApiError && (error.status === 409 || error.status === 412)) {
    return { tone: 'warn', title: 'This changed since it loaded', text: `${error.message} atrium reloaded the list.` };
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

function failed(error: unknown): void {
  const problem = problemOf(error);
  toast(`${problem.title}. ${problem.text}`, { tone: 'error' });
}

export function followToast(channel: MediaChannel, client: QueryClient): void {
  toast(`Following ${channel.name}.`, {
    action: {
      label: 'Undo',
      run: () =>
        void handleUnfollow(channel)
          .then(() => toast(`Stopped following ${channel.name}.`))
          .catch(failed)
          .finally(() => refreshLibrary(client)),
    },
  });
}

export function inputOf(channel: MediaChannel): MediaChannelInput {
  return {
    name: channel.name,
    ...(channel.imdb ? { imdb: channel.imdb } : {}),
    ...(channel.pattern ? { pattern: channel.pattern } : {}),
    case_sensitive: channel.case_sensitive,
    ...(channel.tmdb_id ? { tmdb_id: channel.tmdb_id } : {}),
  };
}

export function unfollowToast(channel: MediaChannel, client: QueryClient): void {
  toast(`Stopped following ${channel.name}.`, {
    action: {
      label: 'Undo',
      run: () =>
        void handleFollow(inputOf(channel))
          .then(() => toast(`Following ${channel.name} again.`))
          .catch(failed)
          .finally(() => refreshLibrary(client)),
    },
  });
}

export function sizeText(size: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = size;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit += 1;
  }
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
}

export function episodeText(season?: number | string, episode?: number | string): string | undefined {
  if (season === undefined || season === '' || episode === undefined || episode === '') return undefined;
  return `S${String(season).padStart(2, '0')}E${String(episode).padStart(2, '0')}`;
}
