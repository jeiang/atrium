import { SvelteMap } from 'svelte/reactivity';

const EXPAND_KEY = 'atrium.chat.expand-thinking';

function storedExpand(): boolean {
  try {
    return localStorage.getItem(EXPAND_KEY) === '1';
  } catch {
    return false;
  }
}

export const thinking = $state({ expanded: storedExpand() });

const chosen = new SvelteMap<string, boolean>();

export function setExpanded(on: boolean): void {
  try {
    if (on) localStorage.setItem(EXPAND_KEY, '1');
    else localStorage.removeItem(EXPAND_KEY);
  } catch {}
  chosen.clear();
  thinking.expanded = on;
}

export function thoughtOpen(key: string, live: boolean): boolean {
  return chosen.get(key) ?? (thinking.expanded || live);
}

export function onThoughtToggle(event: Event, key: string, live: boolean): void {
  const open = (event.currentTarget as HTMLDetailsElement).open;
  if (open !== thoughtOpen(key, live)) chosen.set(key, open);
}

addEventListener('storage', (event) => {
  if (event.key !== EXPAND_KEY) return;
  chosen.clear();
  thinking.expanded = event.newValue === '1';
});
