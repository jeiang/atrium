import type { Query, QueryClient, QueryKey } from '@tanstack/svelte-query';
import type { WebSession } from '../contract';

declare const __ATRIUM_CONTRACT__: string;

export type CachedSession = Pick<WebSession, 'auth_mode' | 'identity' | 'sign_out'>;

interface Entry {
  key: string;
  queryKey: QueryKey;
  data: unknown;
  updatedAt: number;
}

const NAME = 'atrium-read-cache';
const FORMAT = `1:${__ATRIUM_CONTRACT__}`;
const MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000;
const PRIVATE: Record<string, true> = {
  session: true,
  settings: true,
  connections: true,
  auth: true,
  tokens: true,
  secrets: true,
};

let database: Promise<IDBDatabase | undefined> | undefined;
let subject: string | undefined;
let enabled = true;
let watching = false;

function done(tx: IDBTransaction): Promise<void> {
  const { promise, resolve, reject } = Promise.withResolvers<void>();
  tx.oncomplete = () => resolve();
  tx.onerror = () => reject(tx.error);
  tx.onabort = () => reject(tx.error);
  return promise;
}

function open(): Promise<IDBDatabase | undefined> {
  if (database) return database;
  const { promise, resolve } = Promise.withResolvers<IDBDatabase | undefined>();
  database = promise;
  let request: IDBOpenDBRequest;
  try {
    request = indexedDB.open(NAME, 1);
  } catch {
    resolve(undefined);
    return promise;
  }
  request.onupgradeneeded = () => {
    request.result.createObjectStore('entries', { keyPath: 'key' });
    request.result.createObjectStore('meta');
  };
  request.onsuccess = () => {
    const db = request.result;
    db.onversionchange = () => {
      db.close();
      if (database === promise) database = undefined;
    };
    resolve(db);
  };
  request.onerror = () => resolve(undefined);
  request.onblocked = () => resolve(undefined);
  return promise;
}

async function transact(
  mode: IDBTransactionMode,
  work: (entries: IDBObjectStore, meta: IDBObjectStore) => void,
): Promise<boolean> {
  const db = await open();
  if (!db) return false;
  try {
    const tx = db.transaction(['entries', 'meta'], mode);
    work(tx.objectStore('entries'), tx.objectStore('meta'));
    await done(tx);
    return true;
  } catch {
    return false;
  }
}

function persistable(query: Query): boolean {
  const head = query.queryKey[0];
  if (typeof head !== 'string' || PRIVATE[head]) return false;
  const meta = query.meta;
  if (meta?.persist !== undefined) return meta.persist;
  return (meta?.topics?.length ?? 0) > 0;
}

async function current(): Promise<{ format?: string; session?: CachedSession; seen?: number }> {
  let format: IDBRequest<string | undefined> | undefined;
  let session: IDBRequest<CachedSession | undefined> | undefined;
  let seen: IDBRequest<number | undefined> | undefined;
  const ok = await transact('readonly', (_, meta) => {
    format = meta.get('format');
    session = meta.get('session');
    seen = meta.get('seen');
  });
  return ok ? { format: format?.result, session: session?.result, seen: seen?.result } : {};
}

async function entries(): Promise<Entry[]> {
  const now = Date.now();
  let kept: Entry[] = [];
  await transact('readwrite', (store) => {
    const request = store.getAll() as IDBRequest<Entry[]>;
    request.onsuccess = () => {
      kept = [];
      try {
        for (const entry of request.result) {
          if (entry.updatedAt <= now && now - entry.updatedAt < MAX_AGE_MS) kept.push(entry);
          else store.delete(entry.key);
        }
      } catch {
        kept = [];
      }
    };
  });
  return kept;
}

export async function cachedSession(): Promise<CachedSession | undefined> {
  if (!enabled) return undefined;
  const { format, session, seen = 0 } = await current();
  const now = Date.now();
  if (format === FORMAT && seen <= now && now - seen < MAX_AGE_MS) return session;
  if (format !== undefined) await wipe();
  return undefined;
}

function entryOf(query: Query): Entry {
  return {
    key: JSON.stringify(query.queryKey),
    queryKey: query.queryKey,
    data: query.state.data,
    updatedAt: query.state.dataUpdatedAt,
  };
}

export async function restore(client: QueryClient, session: CachedSession, verified: boolean): Promise<void> {
  if (!enabled) return;
  const known = await current();
  const same = known.format === FORMAT && known.session?.identity.subject === session.identity.subject;
  const stored: CachedSession = {
    auth_mode: session.auth_mode,
    identity: session.identity,
    sign_out: session.sign_out,
  };
  const loaded = client
    .getQueryCache()
    .getAll()
    .filter((query) => query.state.status === 'success' && persistable(query))
    .map(entryOf);
  await transact('readwrite', (store, meta) => {
    if (!same) store.clear();
    for (const entry of loaded) store.put(entry);
    meta.put(FORMAT, 'format');
    meta.put(stored, 'session');
    if (verified) meta.put(Date.now(), 'seen');
  });
  if (!enabled) return;
  subject = session.identity.subject;
  for (const entry of same ? await entries() : []) {
    if ((client.getQueryState(entry.queryKey)?.dataUpdatedAt ?? 0) < entry.updatedAt) {
      client.setQueryData(entry.queryKey, entry.data, { updatedAt: entry.updatedAt });
    }
  }
  watch(client);
}

function watch(client: QueryClient): void {
  if (watching) return;
  watching = true;
  client.getQueryCache().subscribe((event) => {
    if (event.type !== 'updated' || event.action.type !== 'success' || event.action.manual) return;
    if (!enabled || subject === undefined || !persistable(event.query)) return;
    const entry = entryOf(event.query);
    void transact('readwrite', (store) => {
      store.put(entry);
    });
  });
}

export async function wipe(): Promise<void> {
  subject = undefined;
  const pending = database;
  database = undefined;
  (await pending)?.close();
  const { promise, resolve } = Promise.withResolvers<void>();
  try {
    const request = indexedDB.deleteDatabase(NAME);
    request.onsuccess = () => resolve();
    request.onerror = () => resolve();
    request.onblocked = () => resolve();
  } catch {
    resolve();
  }
  await promise;
}

export function setPersistence(on: boolean): void {
  enabled = on;
}
