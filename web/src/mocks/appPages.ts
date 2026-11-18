import { HttpResponse, http } from 'msw';
import type { ServerOpsPage, WeatherPage, WeatherPageState } from '../contract';
import { API_BASE } from '../contract';
import { weather } from '../gallery/assistant-fixtures';
import { weatherPlace } from './connections';

export interface WeatherScenario {
  state?: WeatherPageState;
  error?: boolean;
}

export interface ServerOpsScenario {
  empty?: boolean;
  noGrafana?: boolean;
  error?: boolean;
}

const conditions = [
  [3, 'Overcast'],
  [61, 'Slight rain'],
  [2, 'Partly cloudy'],
  [80, 'Slight rain showers'],
  [1, 'Mainly clear'],
  [45, 'Fog'],
  [0, 'Clear sky'],
] as const;

export function weatherPage(state: WeatherPageState = 'ok'): WeatherPage {
  const now = Math.floor(Date.now() / 1000);
  const empty: WeatherPage = { state, fetched_at: null, failure: null, hours: [], days: [] };
  if (state === 'off' || state === 'unconfigured') return empty;
  if (state === 'loading') return { ...empty, location: weather.location, timezone: weather.timezone };
  if (state === 'failed')
    return { ...empty, location: weather.location, timezone: weather.timezone, failure: 'timeout' };
  const hours = Array.from({ length: 24 }, (_, i) => {
    const [code, condition] = conditions[i % conditions.length] ?? conditions[0];
    const hour = (9 + i) % 24;
    const day = 19 + Math.floor((9 + i) / 24);
    return {
      time: `2026-11-${day}T${String(hour).padStart(2, '0')}:00`,
      temperature_c: 6.4 + Math.sin(i / 4) * 2.5,
      precipitation_probability: (i * 13) % 90,
      weather_code: code,
      condition,
    };
  });
  const days = Array.from({ length: 7 }, (_, i) => {
    const [code, condition] = conditions[(i + 1) % conditions.length] ?? conditions[0];
    return {
      date: `2026-11-${19 + i}`,
      high_c: 8.2 + i * 0.4,
      low_c: 2.7 - i * 0.3,
      precipitation_probability: (i * 17 + 20) % 100,
      weather_code: code,
      condition,
      sunrise: `2026-11-${19 + i}T08:0${i}`,
      sunset: `2026-11-${19 + i}T16:4${i}`,
    };
  });
  return {
    state,
    failure: null,
    fetched_at: state === 'stale' ? now - 3 * 3600 : now,
    location: weatherPlace() ?? weather.location,
    timezone: weather.timezone,
    current: weather.current,
    hours,
    days,
  };
}

export function serverOpsPage(scenario: ServerOpsScenario = {}): ServerOpsPage {
  const now = Math.floor(Date.now() / 1000);
  const grafana_url = scenario.noGrafana ? null : 'https://grafana.example.com';
  if (scenario.empty) return { hosts: [], findings: [], grafana_url };
  return {
    grafana_url,
    hosts: [
      {
        host: 'larch',
        sampled_at: now - 420,
        up: false,
        cpu_pct: 71.2,
        mem_pct: 61.5,
        disk_root_pct: 40,
        failed_units: 1,
        alerts: ['HostDown', 'HighCpu'],
      },
      {
        host: 'rowan',
        sampled_at: now - 420,
        up: true,
        cpu_pct: 8.4,
        mem_pct: 33.9,
        disk_root_pct: 72.1,
        failed_units: 0,
        alerts: [],
      },
    ],
    findings: [
      {
        id: 68,
        fact: 'larch:up',
        severity: 'critical',
        title: 'Host larch is down',
        body: 'up changed from up to down at the last sample.',
        updated_at: now - 400,
        expires_at: now + 30 * 86400,
      },
      {
        id: 69,
        fact: 'rowan:disk_root_pct',
        severity: 'warning',
        title: 'Root disk on rowan is filling',
        body: 'disk_root_pct rose from 61% to 72% over a day.',
        updated_at: now - 7200,
        expires_at: now + 29 * 86400,
      },
    ],
  };
}

export function appPageHandlers(read: () => { weather?: WeatherScenario; serverOps?: ServerOpsScenario }) {
  const failed = () =>
    HttpResponse.json({ error: { code: 'unavailable', message: 'atrium is busy.' } }, { status: 503 });
  return [
    http.get(`*${API_BASE}/weather`, () => {
      const scenario = read().weather ?? {};
      if (scenario.error) return failed();
      return HttpResponse.json({ data: weatherPage(scenario.state), revision: '1' });
    }),
    http.get(`*${API_BASE}/ops`, () => {
      const scenario = read().serverOps ?? {};
      if (scenario.error) return failed();
      return HttpResponse.json({ data: serverOpsPage(scenario), revision: '1' });
    }),
  ];
}
