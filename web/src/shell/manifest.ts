import { createQuery, type QueryClient } from '@tanstack/svelte-query';
import { screens } from '../apps';
import { type AppEntry, appsList, type Binding, ok } from '../contract';
import type { Tone } from '../shared/ui/Badge.svelte';
import { p } from './router';

const APPS_TOPICS = ['settings', 'connections'] as const;
const base = import.meta.env.BASE_URL.replace(/\/$/, '');

export const appsQuery = {
  queryKey: ['apps'],
  queryFn: async ({ signal }: { signal: AbortSignal }) => ok(await appsList({ signal })).data.apps,
  meta: { topics: APPS_TOPICS },
};

export function useApps(enabled: () => boolean = () => true) {
  return createQuery(() => ({ ...appsQuery, enabled: enabled() }));
}

export function openApps(apps: readonly AppEntry[] | undefined): AppEntry[] {
  return (apps ?? []).filter((app) => app.enabled);
}

export function refreshApps(client: QueryClient): void {
  void client.invalidateQueries({ queryKey: ['apps'] });
  void client.invalidateQueries({ queryKey: ['shell'] });
}

export interface NavLink {
  label: string;
  href: string;
  leaves: boolean;
}

export function navLinks(app: AppEntry): NavLink[] {
  return app.nav.map((item) => {
    const path: string | undefined = screens[item.url];
    return { label: item.label, href: path ? `${base}${path}` : item.url, leaves: path === undefined };
  });
}

export function unmetNeeds(app: AppEntry): Binding[] {
  return app.needs.filter((need) => !need.met && !need.soft);
}

export function repairHref(need: Binding): string {
  const at = need.connection?.indexOf('/') ?? -1;
  if (!need.connection || at < 0) return p('/settings/connections');
  return p('/settings/connections/:kind/:name', {
    params: { kind: need.connection.slice(0, at), name: need.connection.slice(at + 1) },
  });
}

export const stateWords: Record<string, { label: string; tone: Tone }> = {
  enabled: { label: 'On', tone: 'ok' },
  off: { label: 'Off', tone: 'neutral' },
  'needs-settings': { label: 'Needs settings', tone: 'warn' },
  unavailable: { label: 'Unavailable', tone: 'error' },
};

export function stateOf(app: AppEntry): { label: string; tone: Tone } {
  return stateWords[app.state] ?? { label: app.state.replaceAll('-', ' '), tone: 'neutral' };
}
