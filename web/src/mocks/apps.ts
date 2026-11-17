import { delay, HttpResponse, http } from 'msw';
import type { AppEntry, AppLink, AppNav, Binding } from '../contract';
import { API_BASE } from '../contract';

export interface AppsScenario {
  off?: string[];
  on?: string[];
  refuse?: { code: string; message: string };
  stale?: boolean;
  error?: boolean;
  delay?: number;
}

interface MockApp {
  id: string;
  name: string;
  enabled: boolean;
  switchable: boolean;
  pin?: string;
  state?: 'needs-settings' | 'unavailable';
  reason?: string;
  depends: string[];
  needs: Binding[];
  nav: AppNav[];
}

function need(module: string, connection: string, capability: string, met: boolean, extra: Partial<Binding> = {}) {
  const [kind = connection] = connection.split('/');
  return {
    module,
    active: true,
    kind,
    capability,
    soft: false,
    features: [],
    candidates: [connection],
    connection,
    state: met ? 'ok' : 'failing',
    met,
    availability: met ? 'available' : 'unavailable',
    ...extra,
  } satisfies Binding;
}

export function seedApps(): MockApp[] {
  return [
    {
      id: 'mail',
      name: 'Mail',
      enabled: true,
      switchable: true,
      depends: [],
      needs: [need('mail', 'mail/garden-club', 'imap', true)],
      nav: [
        { label: 'Mail decisions', url: '/triage', settings: false },
        { label: 'Mail rules', url: '/rules', settings: false },
        { label: 'Mail settings', url: '/settings/mail', settings: true },
      ],
    },
    {
      id: 'calendar',
      name: 'Calendar',
      enabled: true,
      switchable: false,
      pin: 'host',
      depends: [],
      needs: [need('calendar', 'google/calendar', 'calendar', true)],
      nav: [],
    },
    {
      id: 'media',
      name: 'Media',
      enabled: true,
      switchable: true,
      state: 'needs-settings',
      reason: 'The library server refuses the saved key.',
      depends: [],
      needs: [
        need('media', 'media/jellyfin', 'library', false, { reason: 'HTTP 401 from /System/Info' }),
        need('media', 'media/qbittorrent', 'downloads', true),
      ],
      nav: [
        { label: 'Downloads', url: '/apps/media/downloads', settings: false },
        { label: 'Library', url: '/apps/media/library', settings: false },
      ],
    },
    {
      id: 'grafana',
      name: 'Grafana',
      enabled: false,
      switchable: true,
      depends: [],
      needs: [need('grafana', 'service/grafana', 'dashboards', true, { soft: true })],
      nav: [{ label: 'Dashboards', url: '/apps/grafana', settings: false }],
    },
    {
      id: 'parcels',
      name: 'Parcels',
      enabled: false,
      switchable: true,
      depends: ['mail'],
      needs: [],
      nav: [{ label: 'Parcels', url: '/watches', settings: false }],
    },
    {
      id: 'orchard',
      name: 'Orchard',
      enabled: true,
      switchable: true,
      depends: [],
      needs: [],
      nav: [{ label: 'Trees', url: '/apps/orchard/trees', settings: false }],
    },
  ];
}

function link(app: MockApp): AppLink {
  return { id: app.id, name: app.name, enabled: app.enabled };
}

export function entries(apps: readonly MockApp[], revision: string): AppEntry[] {
  return apps.map((app) => {
    const depends = apps.filter((other) => app.depends.includes(other.id));
    const unmet = depends.filter((other) => !other.enabled).map((other) => other.id);
    const state = !app.enabled ? 'off' : (app.state ?? (unmet.length > 0 ? 'unavailable' : 'enabled'));
    return {
      id: app.id,
      name: app.name,
      enabled: app.enabled,
      state,
      ...(app.reason && app.enabled ? { reason: app.reason } : {}),
      switchable: app.switchable,
      ...(app.pin ? { pin_source: app.pin } : {}),
      depends: depends.map(link),
      unmet,
      dependents: apps.filter((other) => other.depends.includes(app.id)).map(link),
      needs: app.needs,
      nav: app.nav,
      revision,
    };
  });
}

function refused(code: string, message: string, status = 409): Response {
  return HttpResponse.json({ error: { code, message } }, { status });
}

export function appHandlers(read: () => { apps?: AppsScenario }) {
  let store: MockApp[] = [];
  let seededFrom: string | undefined;
  let revision = 1;
  let staleUsed = false;

  const current = (): MockApp[] => {
    const scenario = read().apps ?? {};
    const key = JSON.stringify(scenario);
    if (key !== seededFrom) {
      seededFrom = key;
      staleUsed = false;
      store = seedApps().map((app) => ({
        ...app,
        enabled: scenario.off?.includes(app.id) ? false : scenario.on?.includes(app.id) ? true : app.enabled,
      }));
    }
    return store;
  };

  return [
    http.get(`*${API_BASE}/apps`, () => {
      const apps = current();
      if (read().apps?.error) return refused('unavailable', 'The app states are not evaluated yet.', 503);
      return HttpResponse.json({ data: { apps: entries(apps, String(revision)) }, revision: String(revision) });
    }),
    http.put(`*${API_BASE}/apps/:id/enabled`, async ({ request, params }) => {
      const apps = current();
      const scenario = read().apps ?? {};
      if (scenario.delay) await delay(scenario.delay);
      const body = (await request.json()) as { enabled?: boolean };
      const app = apps.find((item) => item.id === params.id);
      if (!app) return refused('not_found', `There is no app named ${String(params.id)}.`, 404);
      if (typeof body.enabled !== 'boolean') return refused('invalid', 'Say whether the app is enabled.', 422);
      const expected = request.headers.get('If-Match')?.replace(/^W\//, '').replaceAll('"', '');
      if ((scenario.stale && !staleUsed) || (expected && expected !== String(revision))) {
        staleUsed = true;
        revision += 1;
        return HttpResponse.json(
          {
            error: { code: 'precondition', message: 'The settings changed since they were read.' },
            revision: String(revision),
          },
          { status: 412 },
        );
      }
      if (scenario.refuse) return refused(scenario.refuse.code, scenario.refuse.message);
      if (!app.switchable) return refused('pinned', `${app.name} is set by the host.`);
      if (body.enabled) {
        const off = apps.filter((other) => app.depends.includes(other.id) && !other.enabled);
        if (off.length > 0) {
          return refused('dependency_off', `${app.name} needs ${off.map((o) => o.name).join(' and ')} on first.`);
        }
      } else {
        const on = apps.filter((other) => other.depends.includes(app.id) && other.enabled);
        if (on.length > 0) {
          return refused(
            'dependent_on',
            `${on.map((o) => o.name).join(' and ')} needs ${app.name}. Turn it off first.`,
          );
        }
      }
      app.enabled = body.enabled;
      revision += 1;
      const entry = entries(apps, String(revision)).find((item) => item.id === app.id);
      return HttpResponse.json({ data: entry, undoable: false });
    }),
  ];
}
