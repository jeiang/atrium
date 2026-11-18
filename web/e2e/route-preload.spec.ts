import { resolve } from 'node:path';
import { fakeApi, signedInApi } from './api.ts';
import { test as base, expect } from './fixtures.ts';
import { serve } from './server.ts';

const dist = resolve(process.env.ATRIUM_WEB_DIST ?? 'dist');

const test = base.extend<{ site: string }>({
  site: async ({ browserName: _ }, use) => {
    const { url, server } = await serve(dist, { handle: fakeApi(signedInApi()) });
    await use(url);
    server.closeAllConnections();
    const { promise, resolve: closed } = Promise.withResolvers<void>();
    server.close(() => closed());
    await promise;
  },
});

test.use({ allowStatus: [404], allowConsole: [/Failed to load resource|status of 404/] });

const deepLinks = [
  { path: '/inbox', chunks: [/^\/assets\/InboxPage-[\w-]+\.js$/] },
  { path: '/knowledge', chunks: [/^\/assets\/KnowledgePage-[\w-]+\.js$/] },
  { path: '/chat/1', chunks: [/^\/assets\/ChatPage-[\w-]+\.js$/, /^\/assets\/thread-[\w-]+\.js$/] },
];

for (const { path, chunks } of deepLinks) {
  test(`a deep link to ${path} requests its own route code before the shell data`, async ({ page, site }) => {
    const requested: string[] = [];
    page.on('request', (request) => requested.push(new URL(request.url()).pathname));
    await page.goto(`${site}${path}`);
    await expect.poll(() => chunks.every((chunk) => requested.some((url) => chunk.test(url)))).toBe(true);
    const shell = requested.indexOf('/web/v1/shell');
    expect(shell, requested.join(' ')).toBeGreaterThanOrEqual(0);
    for (const chunk of chunks) {
      expect(
        requested.findIndex((url) => chunk.test(url)),
        `${chunk} before /web/v1/shell in ${requested.join(' ')}`,
      ).toBeLessThan(shell);
    }
    expect(requested.filter((url) => /^\/assets\/HomePage-/.test(url))).toEqual([]);
  });
}
