import { flushSync } from 'svelte';
import {
  onUnauthorized,
  setCsrfToken,
  type WebSession,
  type WebSignIn,
  webSession,
  webSessionLogout,
} from '../contract';
import { queryClient } from '../shared/query';
import { toast } from '../shared/ui/toast.svelte';

export type SessionView =
  | { status: 'loading' }
  | { status: 'signed-in'; session: WebSession }
  | { status: 'redirecting' }
  | { status: 'signed-out'; signIn?: WebSignIn; after: 'logout' | 'start' }
  | { status: 'error'; unavailable: boolean };

export const session = $state<{ view: SessionView }>({ view: { status: 'loading' } });

const REDIRECT_KEY = 'atrium.sign-in';
const REDIRECT_WINDOW_MS = 120_000;
const channel = typeof BroadcastChannel === 'function' ? new BroadcastChannel('atrium-session') : undefined;

export function loginTarget(signIn: WebSignIn | undefined): URL | undefined {
  if (!signIn?.login_url) return undefined;
  let url: URL;
  try {
    url = new URL(signIn.login_url, location.origin);
  } catch {
    return undefined;
  }
  if (url.origin !== location.origin) return undefined;
  url.searchParams.set('next', location.pathname + location.search);
  return url;
}

function recentlyRedirected(): boolean {
  const stamp = Number(sessionStorage.getItem(REDIRECT_KEY));
  return stamp > 0 && Date.now() - stamp < REDIRECT_WINDOW_MS;
}

function forget(): void {
  setCsrfToken(undefined);
  queryClient.clear();
}

function signedOut(signIn: WebSignIn | undefined, redirect: boolean, after: 'logout' | 'start'): void {
  forget();
  const target = redirect ? loginTarget(signIn) : undefined;
  if (target && !recentlyRedirected()) {
    sessionStorage.setItem(REDIRECT_KEY, String(Date.now()));
    flushSync(() => {
      session.view = { status: 'redirecting' };
    });
    location.assign(target.href);
    return;
  }
  session.view = { status: 'signed-out', signIn, after };
}

export async function loadSession(redirect = true, after: 'logout' | 'start' = 'start'): Promise<void> {
  session.view = { status: 'loading' };
  try {
    const res = await webSession();
    if (res.status === 200) {
      setCsrfToken(res.data.csrf_token);
      session.view = { status: 'signed-in', session: res.data };
      return;
    }
    if (res.status === 401) {
      signedOut(res.data.sign_in, redirect, after);
      return;
    }
    session.view = { status: 'error', unavailable: res.status === 503 };
  } catch {
    session.view = { status: 'error', unavailable: false };
  }
}

export async function handleSignOut(): Promise<void> {
  if (session.view.status !== 'signed-in' || !session.view.session.sign_out) return;
  try {
    const res = await webSessionLogout();
    if (res.status !== 204 && res.status !== 401) throw new Error(`HTTP ${res.status}`);
  } catch {
    toast('Could not sign out. Try again.', { tone: 'error' });
    return;
  }
  forget();
  channel?.postMessage('signed-out');
  await loadSession(false, 'logout');
}

onUnauthorized((signIn) => {
  if (session.view.status === 'signed-in') signedOut(signIn, true, 'start');
});

channel?.addEventListener('message', (event: MessageEvent<unknown>) => {
  if (event.data === 'signed-out' && session.view.status === 'signed-in') void loadSession(false, 'logout');
});
