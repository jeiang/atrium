const clock = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
const day = new Intl.DateTimeFormat(undefined, { weekday: 'short', day: 'numeric', month: 'short' });
const dayTime = new Intl.DateTimeFormat(undefined, {
  weekday: 'short',
  day: 'numeric',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
  hourCycle: 'h23',
});

function date(value: string | number | Date): Date {
  if (value instanceof Date) return value;
  return typeof value === 'number' ? new Date(value * 1000) : new Date(value);
}

export function formatClock(value: string | number | Date): string {
  return clock.format(date(value));
}

export function formatDay(value: string | number | Date): string {
  return day.format(date(value));
}

export function formatDayTime(value: string | number | Date): string {
  return dayTime.format(date(value));
}

export function formatSpan(start: string, end: string, allDay = false): string {
  if (allDay) return `${formatDay(start)}, all day`;
  const sameDay = formatDay(start) === formatDay(end);
  return sameDay
    ? `${formatDayTime(start)} to ${formatClock(end)}`
    : `${formatDayTime(start)} to ${formatDayTime(end)}`;
}

export function webHref(url: string): string | undefined {
  try {
    const parsed = new URL(url);
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.href : undefined;
  } catch {
    return undefined;
  }
}

export function plural(count: number, word: string, many = `${word}s`): string {
  return `${count} ${count === 1 ? word : many}`;
}
