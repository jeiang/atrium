import { createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  authBearerIssue,
  authBearerList,
  authBearerRevoke,
  type Issued,
  ok,
  type Token,
} from '../../contract';
import { formatDay } from '../../shared/assistant/format';
import { outcomeUnknown, refusedOffline } from '../../shared/device.svelte';

const KEY = ['settings', 'tokens'] as const;

export function useTokens() {
  return createQuery(() => ({
    queryKey: KEY,
    queryFn: async ({ signal }) => ok(await authBearerList({ signal })),
    meta: { topics: ['settings'], persist: false },
  }));
}

export function refreshTokens(client: QueryClient): Promise<void> {
  return client.invalidateQueries({ queryKey: KEY });
}

export async function handleIssueToken(input: { name: string; expires?: string; agent: boolean }): Promise<Issued> {
  const body = {
    name: input.name,
    agent: input.agent || undefined,
    expires_at: input.expires ? endOfDay(input.expires) : undefined,
  };
  return ok(await authBearerIssue(body, { headers: { 'Idempotency-Key': crypto.randomUUID() } })).data;
}

export async function handleRevokeToken(id: number): Promise<void> {
  ok(await authBearerRevoke(id, { headers: { 'Idempotency-Key': crypto.randomUUID() } }));
}

export function endOfDay(date: string): string {
  const [year, month, day] = date.split('-').map(Number);
  return new Date(year ?? 0, (month ?? 1) - 1, day ?? 1, 23, 59, 59).toISOString();
}

export function tomorrow(now = new Date()): string {
  const next = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1);
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${next.getFullYear()}-${pad(next.getMonth() + 1)}-${pad(next.getDate())}`;
}

export function active(token: Token, now = Date.now()): boolean {
  return !token.revoked_at && (!token.expires_at || Date.parse(token.expires_at) > now);
}

export function tokenMeta(token: Token, now = Date.now()): string {
  const parts = [token.kind === 'oidc' ? `From the sign-in provider for ${token.subject}` : 'Made in atrium'];
  if (token.agent) parts.push('agent');
  parts.push(`made ${formatDay(token.created_at)}`);
  if (token.revoked_at) parts.push(`revoked ${formatDay(token.revoked_at)}`);
  else if (token.expires_at && Date.parse(token.expires_at) <= now)
    parts.push(`expired ${formatDay(token.expires_at)}`);
  else parts.push(token.expires_at ? `expires ${formatDay(token.expires_at)}` : 'never expires');
  parts.push(token.last_used_at ? `last used ${formatDay(token.last_used_at)}` : 'never used');
  return parts.join(' · ');
}

export function tokenFailure(error: unknown): string {
  if (refusedOffline(error) && error instanceof Error) return error.message;
  if (error instanceof TypeError) {
    return 'The outcome is unknown. The connection failed before atrium answered, so this may or may not have happened. atrium did not try again.';
  }
  if (outcomeUnknown(error)) return `The outcome is unknown. ${error.message} atrium did not try again.`;
  if (error instanceof ApiError && error.status === 403) return 'This sign-in cannot change API tokens.';
  return error instanceof Error ? error.message : 'Something went wrong.';
}
