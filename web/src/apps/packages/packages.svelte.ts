import { createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  ok,
  type Package,
  type PackageAdd,
  type PackagesPage,
  packagesAdd,
  packagesDismiss,
  packagesList,
  packagesPoll,
  packagesTrack,
  packagesUntrack,
  type QuotaStatus,
  reviewUndoEffect,
} from '../../contract';
import { outcomeUnknown, refusedOffline } from '../../shared/device.svelte';
import type { Tone } from '../../shared/ui/Badge.svelte';

export const PACKAGE_TOPICS = ['effects', 'mail', 'jobs'] as const;
const PAGE = 100;
const MAX_PAGES = 20;

export interface PackageList {
  items: Package[];
  quota: QuotaStatus;
  ship24: boolean;
}

export async function readPackages(signal: AbortSignal): Promise<PackageList> {
  const items: Package[] = [];
  let first: PackagesPage | undefined;
  let cursor: string | undefined;
  for (let page = 0; page < MAX_PAGES; page++) {
    const data = ok(await packagesList({ limit: PAGE, ...(cursor ? { cursor } : {}) }, { signal })).data;
    first ??= data;
    items.push(...data.items);
    if (!data.next_cursor) break;
    cursor = data.next_cursor;
  }
  return {
    items,
    quota: first?.quota ?? { used: 0, limit: 0, remaining: 0 },
    ship24: first?.ship24_connected ?? false,
  };
}

export function usePackages(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['packages'],
    queryFn: ({ signal }: { signal: AbortSignal }) => readPackages(signal),
    enabled: enabled(),
    meta: { topics: PACKAGE_TOPICS },
  }));
}

export function refreshPackages(client: QueryClient): void {
  void client.invalidateQueries({ queryKey: ['packages'] });
}

export type Stage = 'ordered' | 'shipped' | 'in_transit' | 'out_for_delivery' | 'delivered';
export const stages: readonly { key: Stage; label: string }[] = [
  { key: 'ordered', label: 'Ordered' },
  { key: 'shipped', label: 'Shipped' },
  { key: 'in_transit', label: 'In transit' },
  { key: 'out_for_delivery', label: 'Out for delivery' },
  { key: 'delivered', label: 'Delivered' },
];

const statusWords: Record<string, { label: string; tone: Tone }> = {
  ordered: { label: 'Ordered', tone: 'neutral' },
  shipped: { label: 'Shipped', tone: 'accent' },
  in_transit: { label: 'In transit', tone: 'accent' },
  out_for_delivery: { label: 'Out for delivery', tone: 'accent' },
  delivered: { label: 'Delivered', tone: 'ok' },
  delayed: { label: 'Delayed', tone: 'warn' },
  exception: { label: 'Problem with delivery', tone: 'error' },
};

export function statusOf(item: Package): { label: string; tone: Tone } {
  return statusWords[item.status] ?? { label: item.status_text || 'Status unknown', tone: 'neutral' };
}

export function reached(item: Package): number {
  const at = stages.findIndex((stage) => stage.key === item.status);
  if (at >= 0) return at;
  return item.status === 'delayed' || item.status === 'exception' ? 2 : 0;
}

export function troubled(item: Package): boolean {
  return item.status === 'delayed' || item.status === 'exception';
}

export function tracked(item: Package): boolean {
  return item.tracker_id !== '';
}

export function fromMail(item: Package): boolean {
  return item.source_message_id > 0;
}

export function titleOf(item: Package): string {
  const who = item.merchant || item.carrier;
  return who ? `${who} ${item.tracking_number}` : item.tracking_number;
}

export function delivered(item: Package): boolean {
  return item.status === 'delivered';
}

export async function handleAdd(input: PackageAdd): Promise<number> {
  return ok(await packagesAdd(input)).data.package_id;
}

export async function handleTrack(id: number) {
  return ok(await packagesTrack(id, {})).data.tracking;
}

export async function handleUntrack(id: number) {
  return ok(await packagesUntrack(id));
}

export async function handleDismiss(id: number): Promise<void> {
  ok(await packagesDismiss(id));
}

export async function handlePoll() {
  return ok(await packagesPoll()).data.poll;
}

export async function handleUndo(effects: readonly number[]): Promise<void> {
  for (const id of effects) ok(await reviewUndoEffect(id));
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
    return { tone: 'warn', title: 'This package is gone', text: `${error.message} atrium reloaded the list.` };
  }
  if (error instanceof ApiError && error.code === 'not_configured') {
    return { tone: 'warn', title: 'Ship24 is not connected', text: `${error.message} Nothing was changed.` };
  }
  if (error instanceof ApiError && error.status === 503) {
    return { tone: 'warn', title: 'Not right now', text: `${error.message} Nothing was changed.` };
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

export function quotaLine(list: PackageList): string {
  if (!list.ship24) return 'Ship24 is not connected, so packages are not tracked.';
  if (list.quota.remaining === 0) return `All ${list.quota.limit} Ship24 lookups this month are used.`;
  return `${list.quota.remaining} of ${list.quota.limit} Ship24 lookups left this month.`;
}

export function trackBlocked(list: PackageList | undefined, offline: boolean): string | undefined {
  if (offline) return 'Cannot reach atrium';
  if (!list) return undefined;
  if (!list.ship24) return 'Ship24 is not connected';
  if (list.quota.remaining === 0) return 'No Ship24 lookups left this month';
  return undefined;
}
