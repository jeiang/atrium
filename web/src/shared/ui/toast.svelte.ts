import { announce } from '../announce.svelte';

export interface ToastAction {
  label: string;
  run: () => void;
}

export interface Toast {
  id: number;
  message: string;
  tone: 'neutral' | 'error';
  action?: ToastAction;
}

interface Timer {
  remaining: number;
  started: number;
  handle: number | undefined;
}

export const TOAST_MS = 10_000;

export const toasts = $state<Toast[]>([]);
const timers = new Map<number, Timer>();
let nextId = 1;

export function dismissToast(id: number): void {
  const timer = timers.get(id);
  if (timer?.handle) clearTimeout(timer.handle);
  timers.delete(id);
  const index = toasts.findIndex((toast) => toast.id === id);
  if (index >= 0) toasts.splice(index, 1);
}

function run(id: number, timer: Timer): void {
  timer.started = Date.now();
  timer.handle = window.setTimeout(() => dismissToast(id), timer.remaining);
}

export function pauseToast(id: number): void {
  const timer = timers.get(id);
  if (!timer?.handle) return;
  clearTimeout(timer.handle);
  timer.handle = undefined;
  timer.remaining = Math.max(0, timer.remaining - (Date.now() - timer.started));
}

export function resumeToast(id: number): void {
  const timer = timers.get(id);
  if (timer && !timer.handle) run(id, timer);
}

export function toast(message: string, options: { tone?: Toast['tone']; action?: ToastAction } = {}): number {
  const id = nextId++;
  toasts.push({ id, message, tone: options.tone ?? 'neutral', action: options.action });
  const timer: Timer = { remaining: TOAST_MS, started: 0, handle: undefined };
  timers.set(id, timer);
  run(id, timer);
  announce(message, options.tone === 'error' ? 'assertive' : 'polite');
  return id;
}
