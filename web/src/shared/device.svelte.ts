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

function probe(): void {
  webFetch('/session').catch(() => undefined);
}

onReachability((ok) => {
  if (ok === device.online) return;
  device.online = ok;
  if (ok) void queryClient.refetchQueries({ type: 'active' });
});

addEventListener('online', probe);
addEventListener('offline', probe);
addEventListener('storage', (event) => {
  if (event.key === FORGET_KEY) applyForget(event.newValue === '1');
});
