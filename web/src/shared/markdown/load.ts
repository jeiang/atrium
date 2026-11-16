import type { Parse } from './tree';

let loaded: Parse | undefined;
let loading: Promise<Parse> | undefined;

export function loadedParser(): Parse | undefined {
  return loaded;
}

export function loadMarkdown(): Promise<Parse> {
  loading ??= import('./parse').then(
    (module) => {
      loaded = module.parse;
      return module.parse;
    },
    (error: unknown) => {
      loading = undefined;
      throw error;
    },
  );
  return loading;
}

export function preloadMarkdown<T>(page: Promise<T>): Promise<T> {
  return Promise.all([page, loadMarkdown().catch(() => undefined)]).then(([module]) => module);
}
