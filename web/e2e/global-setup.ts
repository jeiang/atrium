import { access } from 'node:fs/promises';
import { join } from 'node:path';
import { serve } from './server.ts';

export default async function globalSetup(): Promise<() => Promise<void>> {
  const dist = process.env.ATRIUM_WEB_MOCK_DIST ?? 'dist-mock';
  await access(join(dist, 'index.html')).catch(() => {
    throw new Error(`no mock build at ${dist}: run npm run build:mock or set ATRIUM_WEB_MOCK_DIST`);
  });
  const { url, server } = await serve(dist);
  process.env.ATRIUM_E2E_URL = url;
  return async () => {
    const { promise, resolve } = Promise.withResolvers<void>();
    server.close(() => resolve());
    await promise;
  };
}
