import type { Locator, Page } from '@playwright/test';
import { expectFocusVisible, expectReducedMotion, expectTouchTargets, gotoShell } from './checks.ts';
import { expect, galleryLayouts, test } from './fixtures.ts';

const cards = ['a-draft', 'a-move', 'a-triage', 'a-unknown', 'a-elsewhere', 'a-blocked'];

async function gotoGallery(page: Page): Promise<void> {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await gotoShell(page, '/__gallery');
  await expect(page.getByRole('heading', { level: 1, name: 'Component gallery' })).toBeVisible();
}

async function showCard(page: Page, id: string): Promise<Locator> {
  const card = page.locator(`[data-approval="${id}"]`);
  await card.evaluate((element) => element.scrollIntoView({ block: 'start', behavior: 'instant' }));
  const row = card.locator('.approval__row');
  if ((await row.count()) > 0 && (await card.getAttribute('data-state')) !== 'elsewhere') {
    await expect(row).not.toHaveAttribute('inert');
  }
  return card;
}

async function press(page: Page, target: Locator, ms: number): Promise<void> {
  const box = await target.boundingBox();
  if (!box) throw new Error('the hold button has no box');
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(ms);
  await page.mouse.up();
}

function log(page: Page): Locator {
  return page.getByTestId('approval-log');
}

test.describe('assistant design checks', () => {
  for (const layout of galleryLayouts.filter((item) => item.touch)) {
    test(`${layout.name}: every armed control meets the touch rule`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await gotoGallery(page);
      for (const id of cards) {
        await showCard(page, id);
        await expectTouchTargets(page, `[data-approval="${id}"]`);
      }
      for (const section of ['tools', 'receipts', 'home', 'changes']) {
        await expectTouchTargets(page, `[data-gallery="${section}"]`);
      }
    });
  }

  test('only opacity moves while holding under reduced motion', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await gotoGallery(page);
    const hold = page
      .locator('[data-gallery="receipts"]')
      .getByRole('button', { name: 'Hold to send the test message' });
    await hold.scrollIntoViewIfNeeded();
    const box = await hold.boundingBox();
    if (!box) throw new Error('no hold button');
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.waitForTimeout(200);
    const fill = await hold.locator('.hold__fill').evaluate((element) => {
      const style = getComputedStyle(element);
      return { transform: style.transform, opacity: Number(style.opacity) };
    });
    await page.mouse.up();
    expect(fill.transform).toBe('none');
    expect(fill.opacity).toBeGreaterThan(0);
    expect(fill.opacity).toBeLessThan(1);
    await expectReducedMotion(page);
  });
});

test.describe('assistant keyboard focus', () => {
  test.use({ viewport: { width: 1440, height: 900 } });
  for (const section of ['tools', 'receipts', 'home']) {
    test(`every stop in ${section} shows the focus ring`, async ({ page }) => {
      await gotoGallery(page);
      await expectFocusVisible(page, `[data-gallery="${section}"]`);
    });
  }

  test('every armed stop in an approval card shows the focus ring', async ({ page }) => {
    await gotoGallery(page);
    await showCard(page, 'a-move');
    expect(await expectFocusVisible(page, '[data-approval="a-move"]')).toBeGreaterThanOrEqual(3);
  });
});

test.describe('hold to send on a touch layout', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });

  test('the hold waits for the end of the change, cancels on release and sends once', async ({ page }) => {
    await gotoGallery(page);
    const card = await showCard(page, 'a-draft');
    const hold = card.getByRole('button', { name: 'Hold to send email to Ilse' });
    await expect(hold).toHaveAttribute('aria-disabled', 'true');
    await expect(card.getByText('More below. Read to the end to send')).toBeVisible();

    await press(page, hold, 1200);
    await expect(hold).not.toHaveAttribute('aria-disabled');
    await expect(log(page)).toHaveText('No decisions yet');

    await press(page, hold, 250);
    await expect(card.getByText('Keep holding to send. Nothing was sent.')).toBeVisible();
    await page.waitForTimeout(800);
    await expect(log(page)).toHaveText('No decisions yet');

    await press(page, hold, 900);
    await expect(log(page)).toHaveText('approve a-draft');
    const receipt = card.locator('.receipt');
    await expect(receipt).toContainText('Sent to Ilse de Wit');
    await expect(receipt).toContainText('It cannot be recalled');
    await expect(receipt.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0);
  });

  test('Enter shows the end, then asks to confirm with Cancel focused', async ({ page }) => {
    await gotoGallery(page);
    const card = await showCard(page, 'a-draft');
    const hold = card.getByRole('button', { name: 'Hold to send email to Ilse' });
    await hold.focus();
    await page.keyboard.press('Enter');
    await expect(hold).not.toHaveAttribute('aria-disabled');
    await expect(page.getByRole('dialog')).toHaveCount(0);

    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog', { name: 'Send this now?' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(dialog).toBeHidden();
    await expect(log(page)).toHaveText('No decisions yet');

    await expect(hold).toBeFocused();
    await page.keyboard.press('Space');
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: 'Send now' }).click();
    await expect(log(page)).toHaveText('approve a-draft');
    await expect(card.locator('.receipt')).toContainText('Sent to Ilse de Wit');
  });

  test('a card decides inside the sheet', async ({ page }) => {
    await gotoGallery(page);
    await page.getByRole('button', { name: 'Open approval in a sheet' }).click();
    const sheet = page.locator('dialog.sheet[open]');
    await expect(sheet.locator('[data-approval="a-sheet"] .approval__row')).not.toHaveAttribute('inert');
    await sheet.getByRole('button', { name: 'Move the event' }).click();
    await expect(sheet.locator('.receipt')).toContainText('Moved to Friday 09:30');
    await expect(log(page)).toHaveText('approve a-sheet');
  });
});

test.describe('approval cards on a desktop', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('the action row is inert away from the screen and re-arms after a moment', async ({ page }) => {
    await gotoGallery(page);
    const card = await showCard(page, 'a-move');
    const row = card.locator('.approval__row');
    const decline = card.getByRole('button', { name: 'Decline' });
    await decline.focus();
    await page.evaluate(() => scrollTo({ top: 0, behavior: 'instant' }));
    await expect(row).toHaveAttribute('inert');
    await page.keyboard.press('Enter');
    await page.keyboard.press('Space');
    await page.waitForTimeout(400);
    await expect(log(page)).toHaveText('No decisions yet');

    await card.evaluate((element) => element.scrollIntoView({ block: 'start', behavior: 'instant' }));
    await expect(row).toHaveAttribute('data-arming');
    await expect(row).toHaveAttribute('inert');
    await expect(row).not.toHaveAttribute('inert');
    await decline.click();
    await expect(log(page)).toHaveText('decline a-move');
    const receipt = card.locator('.receipt');
    await expect(receipt).toContainText('Declined. atrium will not do this');
    await receipt.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(log(page)).toHaveText('decline a-move, undo decline a-move');
    await expect(card.getByRole('button', { name: 'Move the event' })).toBeVisible();
    await expect(card.locator('.receipt')).toHaveCount(0);
  });

  test('a reversible approval leaves a receipt with Undo that fires once', async ({ page }) => {
    await gotoGallery(page);
    const card = await showCard(page, 'a-triage');
    await card.getByRole('button', { name: 'File 3 messages' }).click();
    const undo = card.locator('.receipt').getByRole('button', { name: 'Undo', exact: true });
    await expect(card.locator('.receipt')).toContainText('Filed 3 messages');
    await undo.click();
    await expect(log(page)).toHaveText('approve a-triage, undo approve a-triage');
    await expect(card.getByRole('button', { name: 'File 3 messages' })).toBeVisible();
  });

  test('a send counts down for 10 seconds with Undo before it acts', async ({ page }) => {
    test.setTimeout(45_000);
    await gotoGallery(page);
    const card = await showCard(page, 'a-draft');
    const send = card.getByRole('button', { name: 'Send email to Ilse' });
    await press(page, send, 50);
    await expect(send).not.toHaveAttribute('aria-disabled');
    await press(page, send, 50);
    const pending = card.locator('.receipt[data-tone="pending"]');
    await expect(pending).toContainText(/Sending to Ilse de Wit in (10|9) s/);
    await pending.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(pending).toHaveCount(0);
    await expect(log(page)).toHaveText('No decisions yet');

    await expect(card.locator('.approval__row')).not.toHaveAttribute('inert');
    await card.getByRole('button', { name: 'Send email to Ilse' }).click();
    await page.waitForTimeout(6000);
    await expect(log(page)).toHaveText('No decisions yet');
    await expect(log(page)).toHaveText('approve a-draft', { timeout: 8000 });
    await expect(card.locator('.receipt')).toContainText('Sent to Ilse de Wit');
  });

  test('unknown kinds, decisions made elsewhere and blocked cards still say what they are', async ({ page }) => {
    await gotoGallery(page);
    const unknown = page.locator('[data-approval="a-unknown"]');
    await expect(unknown).toContainText('Start watering');
    await expect(unknown).toContainText('Plot 14, Allotment society');
    await expect(unknown).toContainText('Turns on the drip line at 06:00 tomorrow.');
    await expect(page.locator('[data-gallery="changes"]')).toContainText('"keep_old_days": 7');

    const elsewhere = page.locator('[data-approval="a-elsewhere"]');
    await expect(elsewhere).toContainText(/Decided on iPhone at \d\d:\d\d/);
    await expect(elsewhere.getByRole('button', { name: 'Accept invite' })).toBeDisabled();

    const blocked = await showCard(page, 'a-blocked');
    await expect(blocked).toContainText('The Home calendar is disconnected');
    await blocked.getByRole('button', { name: 'Reconnect calendar' }).click();
    await expect(log(page)).toHaveText('repair a-blocked');

    const move = page.locator('[data-approval="a-move"]');
    await expect(move.locator('del')).toContainText('Dentist check-up');
    await expect(move.locator('ins')).toContainText('Dentist check-up');
  });

  test('a receipt Undo fires once and an expired one offers none', async ({ page }) => {
    await gotoGallery(page);
    const section = page.locator('[data-gallery="receipts"]');
    await expect(section.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(1);
    await section.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(section.getByTestId('undo-count')).toHaveText('Undo pressed 1 times');
    await expect(section.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0);
    await section.getByRole('button', { name: 'Archive with undo toast' }).click();
    await page.locator('.toasts').getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(section.getByTestId('undo-count')).toHaveText('Undo pressed 2 times');
  });

  test('the digest shows five lines and counts the rest', async ({ page }) => {
    await gotoGallery(page);
    const digest = page.locator('[data-gallery="home"] .digest').first();
    await expect(digest.locator('li')).toHaveCount(5);
    await expect(digest).toContainText('7 items');
    await expect(digest).toContainText('and 2 more');
    await expect(digest.getByRole('link', { name: 'Allotment work day is on 5 December' })).toHaveAttribute(
      'href',
      'https://allotment.example/workday',
    );
    await expect(page.locator('[data-gallery="home"]')).toContainText('Feels like 3°');
  });
});

test.describe('tool cards', () => {
  test('each simulator script ends in the matching tool state', async ({ page }) => {
    await gotoGallery(page);
    const section = page.locator('[data-gallery="tools"]');
    const live = section.getByTestId('tool-live');

    await section.getByText('Finishes', { exact: true }).click();
    await section.getByRole('button', { name: 'Run script' }).click();
    await expect(live.locator('[data-tool="call-1"]')).toHaveAttribute('data-state', 'running');
    await expect(section.getByTestId('tool-run-result')).toHaveText('done');
    const done = live.locator('[data-tool="call-1"]');
    await expect(done).toHaveAttribute('data-state', 'done');
    await expect(done).toContainText('Found prices from 3 suppliers · 3 sources');
    const toggle = done.getByRole('button', { name: /search_web/ });
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await toggle.click();
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    const sources = done.getByRole('list', { name: 'Sources' });
    await expect(sources.getByRole('link')).toHaveCount(2);
    await expect(sources.getByRole('link', { name: /Cedar & Pine Yard/ })).toHaveAttribute(
      'rel',
      'noopener noreferrer',
    );
    await expect(sources.getByRole('link', { name: /hillfield-timber/ })).toHaveAttribute(
      'href',
      'https://hillfield-timber.example/price-list',
    );
    await expect(sources).toContainText('Unsafe link that must stay text');

    await section.getByText('Fails', { exact: true }).click();
    await section.getByRole('button', { name: 'Run script' }).click();
    await expect(section.getByTestId('tool-run-result')).toHaveText('failed');
    await expect(live.locator('[data-tool="call-1"]')).toHaveAttribute('data-state', 'failed');
    await expect(live).toContainText('The weather service timed out');

    await section.getByText('Parks', { exact: true }).click();
    await section.getByRole('button', { name: 'Run script' }).click();
    await expect(section.getByTestId('tool-run-result')).toHaveText('parked');
    const waiting = live.locator('[data-tool="call-1"]');
    await expect(waiting).toHaveAttribute('data-state', 'waiting');
    await expect(waiting.locator('[data-approval="a-parked"]')).toContainText(
      'Send the book club reminder to 6 people',
    );
  });
});
