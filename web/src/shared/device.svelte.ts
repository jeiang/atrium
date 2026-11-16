import { onReachability, webFetch } from '../contract';
import { queryClient } from './query';
import { setPersistence, wipe } from './readcache';

const FORGET_KEY = 'atrium.device.forget';

function storedForget(): boolean {
  try {
    return localStorage.getItem(FORGET_KEY) === '1';
  } catch {
    return false;
  }
}

export const device = $state({ online: true, forget: storedForget() });

setPersistence(!device.forget);
if (device.forget) void wipe();

function applyForget(on: boolean): void {
  device.forget = on;
  setPersistence(!on);
  if (on) void wipe();
}

export function setForget(on: boolean): void {
  try {
    if (on) localStorage.setItem(FORGET_KEY, '1');
    else localStorage.removeItem(FORGET_KEY);
  } catch {}
  applyForget(on);
}

const PROBE_MIN_MS = 5_000;
const PROBE_MAX_MS = 60_000;

let probeDelay = PROBE_MIN_MS;
let probeTimer: ReturnType<typeof setTimeout> | undefined;
const reconnected: (() => void)[] = [];

export function onReconnect(handler: () => void): void {
  reconnected.push(handler);
}

function probe(): void {
  webFetch('/session').catch(() => undefined);
}

function schedule(): void {
  clearTimeout(probeTimer);
  probeTimer = setTimeout(() => {
    probeDelay = Math.min(probeDelay * 2, PROBE_MAX_MS);
    probe();
  }, probeDelay);
}

onReachability((ok) => {
  if (!ok) schedule();
  if (ok === device.online) return;
  device.online = ok;
  if (!ok) return;
  clearTimeout(probeTimer);
  probeDelay = PROBE_MIN_MS;
  void queryClient.refetchQueries({ type: 'active' });
  for (const handler of reconnected) handler();
});

addEventListener('online', probe);
addEventListener('offline', probe);
addEventListener('storage', (event) => {
  if (event.key === FORGET_KEY) applyForget(event.newValue === '1');
});
