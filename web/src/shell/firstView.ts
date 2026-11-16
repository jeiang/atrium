import { queryClient } from '../shared/query';
import { shellQuery } from './frame';

const shown = Promise.withResolvers<void>();
let routed = false;
let settled = false;
const check = () => {
  if (!routed || queryClient.isFetching() > 0) return;
  if (queryClient.getQueryState(shellQuery.queryKey)?.status === 'pending') return;
  settle();
};
const settle = () => {
  if (settled) return;
  settled = true;
  stop();
  clearTimeout(cap);
  requestAnimationFrame(() => setTimeout(shown.resolve, 250));
};
const stop = queryClient.getQueryCache().subscribe(check);
const cap = setTimeout(settle, 2000);

export const firstView = shown.promise;

export function routeLoaded(): void {
  routed = true;
  check();
}
