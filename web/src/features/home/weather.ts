import { plural } from '../../shared/assistant/format';

export const STALE_MINUTES = 90;

export function wallClock(zone: string, at: Date): string {
  const format = new Intl.DateTimeFormat('en-CA', {
    timeZone: zone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  });
  const got: Record<string, string> = {};
  for (const part of format.formatToParts(at)) got[part.type] = part.value;
  return `${got.year}-${got.month}-${got.day}T${got.hour}:${got.minute}`;
}

function minutesOf(wall: string): number {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(wall);
  if (!match) return Number.NaN;
  const [, y, mo, d, h, mi] = match.map(Number);
  return Date.UTC(y ?? 0, (mo ?? 1) - 1, d ?? 1, h ?? 0, mi ?? 0) / 60_000;
}

export function ageMinutes(observed: string, zone: string, now: Date): number | undefined {
  let current: string;
  try {
    current = wallClock(zone, now);
  } catch {
    return undefined;
  }
  const age = minutesOf(current) - minutesOf(observed);
  return Number.isFinite(age) ? Math.max(0, age) : undefined;
}

export function ageText(minutes: number): string {
  if (minutes < 120) return `${plural(minutes, 'minute')} ago`;
  if (minutes < 48 * 60) return `${plural(Math.floor(minutes / 60), 'hour')} ago`;
  return `${plural(Math.floor(minutes / 1440), 'day')} ago`;
}
