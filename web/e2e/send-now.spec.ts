import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, layouts, test, useScenario } from './fixtures.ts';

const edgeSurfaces = ['.rail', '.tabbar'];

for (const layout of layouts) {
  test(`${layout.name}: Send now and Unsend work from the composer and the queued list`, async ({ page }) => {
    await page.setViewportSize({ width: layout.width, height: layout.height });
    await useScenario(page, { chat: { speed: 0.05 } });
    await gotoShell(page, '/chat/7');
    const messages = page.getByRole('list', { name: 'Messages' });
    const box = page.getByRole('textbox', { name: 'Reply' });
    await expect(page.getByRole('button', { name: 'Stop run' })).toBeVisible();

    await box.fill('Also price the brass screws.');
    const sent = page.waitForRequest(
      (request) => request.method() === 'POST' && request.url().endsWith('/threads/7/messages'),
    );
    await page.getByRole('button', { name: 'Send now' }).click();
    expect((await sent).postDataJSON()).toEqual({ text: 'Also price the brass screws.', now: true });
    await expect(messages.getByText('Also price the brass screws.')).toBeVisible();
    await expect(messages.getByText('Sending now', { exact: true })).toBeVisible();
    await expect(box).toHaveValue('');
    if (layout.touch) await expectTouchTargets(page, 'main');
    await expectRoundedSurfaces(page, edgeSurfaces);
    await expectNoHorizontalScroll(page);
    await expectNoSeriousAxeFindings(page);

    const unsent = page.waitForRequest((request) => request.method() === 'DELETE');
    await messages.getByRole('button', { name: 'Unsend' }).click();
    expect((await unsent).headers()['idempotency-key']).toMatch(/^[0-9a-f-]{36}$/);
    await expect(messages.getByText('Also price the brass screws.')).toHaveCount(0);
    await expect(box).toHaveValue('Also price the brass screws.');

    await page.getByRole('button', { name: 'Queue' }).click();
    await expect(messages.getByText('Also price the brass screws.')).toBeVisible();
    await expect(messages.getByText('Queued', { exact: true })).toBeVisible();
    await expect(messages.getByRole('button', { name: 'Unsend' })).toBeVisible();
    if (layout.touch) await expectTouchTargets(page, 'main');
    await expectNoHorizontalScroll(page);
    await expectNoSeriousAxeFindings(page);
  });
}
