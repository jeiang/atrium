import type { HomeWeatherFailure } from '../../contract';
import { plural } from '../../shared/assistant/format';

export function ageText(minutes: number): string {
  if (minutes < 120) return `${plural(minutes, 'minute')} ago`;
  if (minutes < 48 * 60) return `${plural(Math.floor(minutes / 60), 'hour')} ago`;
  return `${plural(Math.floor(minutes / 1440), 'day')} ago`;
}

const failures: Record<NonNullable<HomeWeatherFailure>, string> = {
  unreachable: 'The weather service could not be reached.',
  timeout: 'The weather service took too long to answer.',
  no_match: 'The weather service does not know the location.',
  bad_reply: 'The weather service sent an answer atrium could not read.',
  unavailable: 'The weather service is unavailable right now.',
};

export function failureText(failure: HomeWeatherFailure | undefined): string {
  return (failure && failures[failure]) || 'The forecast did not load.';
}
