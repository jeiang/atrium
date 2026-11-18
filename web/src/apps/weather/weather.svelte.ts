import { createQuery } from '@tanstack/svelte-query';
import { ok, weatherPage } from '../../contract';

const WEATHER_TOPICS = ['assistant', 'settings', 'connections'] as const;
export const HOURS_SHOWN = 12;

export function useWeather(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['weather'],
    queryFn: async ({ signal }: { signal: AbortSignal }) => ok(await weatherPage({ signal })).data,
    enabled: enabled(),
    meta: { topics: WEATHER_TOPICS },
  }));
}

const dayWords = new Intl.DateTimeFormat(undefined, {
  weekday: 'short',
  day: 'numeric',
  month: 'short',
  timeZone: 'UTC',
});
const compass = ['N', 'NE', 'E', 'SE', 'S', 'SW', 'W', 'NW'];

export function degrees(value: number): string {
  return `${Math.round(value)}°`;
}

export function hourText(time: string): string {
  return time.slice(11, 16);
}

export function dayText(date: string, index: number): string {
  if (index === 0) return 'Today';
  const at = new Date(`${date}T12:00:00Z`);
  return Number.isNaN(at.getTime()) ? date : dayWords.format(at);
}

export function windText(speed: number, direction: number): string {
  const point = compass[Math.round((((direction % 360) + 360) % 360) / 45) % 8];
  return `${Math.round(speed)} km/h ${point}`;
}
