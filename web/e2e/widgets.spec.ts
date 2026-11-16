import type { Locator, Page } from '@playwright/test';
import {
  expectFocusVisible,
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectReducedMotion,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, galleryLayouts, layouts, test } from './fixtures.ts';

const edgeSurfaces = ['.rail', '.tabbar', 'dialog.sheet'];
const sections = ['menus', 'comboboxes', 'tabs', 'listboxes'] as const;
const phone = layouts[0] ?? { name: 'phone', width: 390, height: 844, touch: true };
const desktop = layouts[2] ?? { name: 'desktop', width: 1440, height: 900, touch: false };

async function gotoGallery(page: Page): Promise<void> {
  await gotoShell(page, '/__gallery');
  await expect(page.getByRole('heading', { level: 1, name: 'Component gallery' })).toBeVisible();
}

async function settle(popup: Locator): Promise<void> {
  await popup.evaluate((el) =>
    Promise.allSettled(el.getAnimations({ subtree: true }).map((animation) => animation.finished)),
  );
}

function keys(page: Page): { tab: string; back: string } {
  const webkit = page.context().browser()?.browserType().name() === 'webkit';
  return { tab: webkit ? 'Alt+Tab' : 'Tab', back: webkit ? 'Alt+Shift+Tab' : 'Shift+Tab' };
}

async function expectInsideViewport(popup: Locator): Promise<{ top: number; bottom: number }> {
  const box = await popup.evaluate((el) => {
    const rect = el.getBoundingClientRect();
    const root = document.documentElement;
    return {
      top: rect.top,
      left: rect.left,
      right: rect.right,
      bottom: rect.bottom,
      width: root.clientWidth,
      height: root.clientHeight,
    };
  });
  expect(box.top, 'popup top inside the viewport').toBeGreaterThanOrEqual(0);
  expect(box.left, 'popup left inside the viewport').toBeGreaterThanOrEqual(0);
  expect(box.right, 'popup right inside the viewport').toBeLessThanOrEqual(box.width);
  expect(box.bottom, 'popup bottom inside the viewport').toBeLessThanOrEqual(box.height);
  return box;
}

async function placeAt(target: Locator, edge: 'top' | 'bottom'): Promise<void> {
  await target.evaluate((el, where) => {
    document.body.style.paddingBlockEnd = '100vh';
    const rect = el.getBoundingClientRect();
    const offset = where === 'top' ? rect.top - 8 : rect.bottom - document.documentElement.clientHeight + 8;
    window.scrollBy({ top: offset, behavior: 'instant' });
  }, edge);
}

const openStates = [
  {
    name: 'menu',
    scope: '[role="menu"]:popover-open',
    open: async (page: Page) => page.getByRole('button', { name: 'Thread actions' }).click(),
  },
  {
    name: 'row overflow menu',
    scope: '[role="menu"]:popover-open',
    open: async (page: Page) => page.getByRole('button', { name: 'More for Plan the garden shed order' }).click(),
  },
  {
    name: 'combobox',
    scope: '.pop:popover-open',
    open: async (page: Page) => {
      await page.getByRole('combobox', { name: 'Folder', exact: true }).click();
      await page.keyboard.press('ArrowDown');
    },
  },
  {
    name: 'async combobox',
    scope: '.pop:popover-open',
    open: async (page: Page) => {
      await page.getByRole('combobox', { name: 'Contact' }).click();
      await page.keyboard.type('r');
      await expect(page.getByRole('option', { name: /Robin Marsh/ })).toBeVisible();
    },
  },
  {
    name: 'listbox sheet',
    scope: 'dialog[open]',
    open: async (page: Page) => page.getByRole('button', { name: 'Move to folder' }).click(),
  },
];

test.describe('widget design checks', () => {
  for (const layout of galleryLayouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      test('each widget section passes the design checks', async ({ page }) => {
        await page.emulateMedia({ reducedMotion: 'reduce' });
        await gotoGallery(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edgeSurfaces);
        await expectNoBlue(page);
        await expectReducedMotion(page);
        await expectNoSeriousAxeFindings(page);
        for (const section of sections) {
          if (layout.touch) await expectTouchTargets(page, `[data-gallery="${section}"]`);
          await expectFocusVisible(page, `[data-gallery="${section}"]`);
        }
      });

      for (const state of openStates) {
        test(`the open ${state.name} passes the design checks`, async ({ page }) => {
          await page.emulateMedia({ reducedMotion: 'reduce' });
          await gotoGallery(page);
          await state.open(page);
          const popup = page.locator(state.scope).first();
          await expect(popup).toBeVisible();
          await settle(popup);
          await expectInsideViewport(popup);
          await expectRoundedSurfaces(page, edgeSurfaces);
          await expectNoBlue(page);
          await expectReducedMotion(page);
          if (layout.touch) await expectTouchTargets(page, state.scope);
          await expectNoSeriousAxeFindings(page);
        });
      }
    });
  }
});

test.describe('menu', () => {
  test.use({ viewport: { width: desktop.width, height: desktop.height } });

  test('the keyboard walk follows the menu button pattern', async ({ page }) => {
    await gotoGallery(page);
    const trigger = page.getByRole('button', { name: 'Thread actions' });
    const menu = page.getByRole('menu', { name: 'Thread actions' });
    const item = (name: string) => menu.getByRole('menuitem', { name });
    const result = page.getByTestId('menu-result');
    await expect(trigger).toHaveAttribute('aria-haspopup', 'menu');
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');
    await trigger.focus();
    await page.keyboard.press('Enter');
    await expect(menu).toBeVisible();
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');
    await expect(item('Rename')).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await expect(item('Pin')).toBeFocused();
    await page.keyboard.press('ArrowUp');
    await page.keyboard.press('ArrowUp');
    await expect(item('Delete')).toBeFocused();
    await page.keyboard.press('Home');
    await expect(item('Rename')).toBeFocused();
    await page.keyboard.press('End');
    await expect(item('Delete')).toBeFocused();
    await page.keyboard.press('m');
    await expect(item('Mute for a day')).toBeFocused();
    await page.waitForTimeout(600);
    await page.keyboard.press('e');
    await expect(item('Export')).toBeFocused();
    await expect(item('Export')).toHaveAttribute('aria-disabled', 'true');
    await page.keyboard.press('Enter');
    await expect(menu).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(menu).toBeHidden();
    await expect(trigger).toBeFocused();
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');
    await expect(result).toHaveText('Nothing chosen');

    await page.keyboard.press(' ');
    await expect(item('Rename')).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Enter');
    await expect(menu).toBeHidden();
    await expect(trigger).toBeFocused();
    await expect(result).toHaveText('Chose pin');

    await page.keyboard.press('ArrowUp');
    await expect(item('Delete')).toBeFocused();
    await page.keyboard.press(keys(page).tab);
    await expect(menu).toBeHidden();
    await expect(trigger).not.toBeFocused();

    await trigger.focus();
    await page.keyboard.press('ArrowDown');
    await expect(item('Rename')).toBeFocused();
    await page.keyboard.press(keys(page).back);
    await expect(menu).toBeHidden();
    await expect(trigger).toBeFocused();
  });

  test('a pointer opens, toggles, picks and dismisses', async ({ page }) => {
    await gotoGallery(page);
    const trigger = page.getByRole('button', { name: 'Thread actions' });
    const menu = page.getByRole('menu', { name: 'Thread actions' });
    await trigger.click();
    await expect(menu).toBeVisible();
    await trigger.click();
    await expect(menu).toBeHidden();
    await trigger.click();
    await page.getByRole('heading', { name: 'Menus' }).click();
    await expect(menu).toBeHidden();

    const overflow = page.getByRole('button', { name: 'More for Book club reminder' });
    await overflow.click();
    const rowMenu = page.getByRole('menu', { name: 'More for Book club reminder' });
    await rowMenu.getByRole('menuitem', { name: 'Archive' }).click();
    await expect(rowMenu).toBeHidden();
    await expect(overflow).toBeFocused();
    await expect(page.getByTestId('menu-result')).toHaveText('Chose archive for Book club reminder');
  });

  test.describe('touch', () => {
    test.use({ viewport: { width: phone.width, height: phone.height }, hasTouch: true });

    test('a tap opens, picks and an outside tap dismisses', async ({ page }) => {
      await gotoGallery(page);
      const overflow = page.getByRole('button', { name: 'More for the shed order' });
      const menu = page.getByRole('menu', { name: 'More for the shed order' });
      await overflow.tap();
      await expect(menu).toBeVisible();
      await menu.getByRole('menuitem', { name: 'Mark as read' }).tap();
      await expect(menu).toBeHidden();
      await expect(page.getByTestId('menu-result')).toHaveText('Chose mark for the shed order');
      await overflow.tap();
      await expect(menu).toBeVisible();
      await page.getByRole('heading', { name: 'Menus' }).tap();
      await expect(menu).toBeHidden();
    });
  });
});

test.describe('combobox', () => {
  test.use({ viewport: { width: desktop.width, height: desktop.height } });

  test('the keyboard walk follows the list autocomplete pattern', async ({ page }) => {
    await gotoGallery(page);
    const input = page.getByRole('combobox', { name: 'Folder', exact: true });
    const list = page.getByRole('listbox', { name: 'Folder', exact: true });
    const result = page.getByTestId('combo-result');
    const activeIs = async (name: string) => {
      const id = await input.getAttribute('aria-activedescendant');
      expect(id).not.toBeNull();
      await expect(page.locator(`[id="${id}"]`)).toContainText(name);
    };
    await expect(input).toHaveAttribute('aria-autocomplete', 'list');
    await expect(input).toHaveAttribute('aria-expanded', 'false');
    await input.focus();
    await page.keyboard.press('ArrowDown');
    await expect(input).toHaveAttribute('aria-expanded', 'true');
    await expect(list).toBeVisible();
    await activeIs('Inbox');
    await page.keyboard.press('ArrowDown');
    await activeIs('Receipts');
    await page.keyboard.press('ArrowUp');
    await page.keyboard.press('ArrowUp');
    await activeIs('Book club');
    await page.keyboard.press('Enter');
    await expect(list).toBeHidden();
    await expect(input).toHaveValue('Book club');
    await expect(input).toBeFocused();
    await expect(result).toContainText('Folder: Book club.');

    await input.fill('');
    await page.keyboard.type('a');
    await expect(list.getByRole('option')).toHaveCount(6);
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowDown');
    await activeIs('Garden club');
    await page.keyboard.press('ArrowDown');
    await activeIs('Family');
    await expect(result).toContainText('Folder: none.');

    await page.keyboard.press('Escape');
    await expect(list).toBeHidden();
    await expect(input).toHaveValue('a');
    await page.keyboard.press('Escape');
    await expect(input).toHaveValue('');

    await page.keyboard.type('zzz');
    await expect(page.getByRole('status').filter({ hasText: 'No matches' })).toBeVisible();
    await page.keyboard.press(keys(page).tab);
    await expect(page.getByRole('listbox', { name: 'Folder', exact: true })).toBeHidden();
    await expect(input).toHaveAttribute('aria-expanded', 'false');
  });

  test('async options load, pick and report failure', async ({ page }) => {
    await gotoGallery(page);
    const input = page.getByRole('combobox', { name: 'Contact' });
    await input.focus();
    await page.keyboard.type('ro');
    await expect(page.getByRole('status').filter({ hasText: 'Searching' })).toBeVisible();
    const list = page.getByRole('listbox', { name: 'Contact' });
    await expect(list.getByRole('option')).toHaveCount(2);
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Enter');
    await expect(input).toHaveValue('Rowan Ellery');
    await expect(page.getByTestId('combo-result')).toContainText('Contact: Rowan Ellery.');
    await input.fill('offline');
    await expect(page.getByText('Search failed. Type again to retry.')).toBeVisible();
  });

  test('a pointer picks an option and an outside press dismisses', async ({ page }) => {
    await gotoGallery(page);
    const input = page.getByRole('combobox', { name: 'Folder', exact: true });
    await input.click();
    await page.keyboard.type('tra');
    await page.getByRole('listbox', { name: 'Folder', exact: true }).getByRole('option', { name: 'Travel' }).click();
    await expect(input).toHaveValue('Travel');
    await expect(input).toBeFocused();
    await expect(page.getByRole('listbox', { name: 'Folder', exact: true })).toBeHidden();
    await page.keyboard.press('ArrowDown');
    await expect(page.getByRole('listbox', { name: 'Folder', exact: true })).toBeVisible();
    await expect(
      page.getByRole('listbox', { name: 'Folder', exact: true }).getByRole('option', { name: 'Travel' }),
    ).toHaveAttribute('aria-selected', 'true');
    await page.getByRole('heading', { name: 'Comboboxes' }).click();
    await expect(page.getByRole('listbox', { name: 'Folder', exact: true })).toBeHidden();
  });

  test.describe('touch', () => {
    test.use({ viewport: { width: phone.width, height: phone.height }, hasTouch: true });

    test('a tap picks an option and an outside tap dismisses', async ({ page }) => {
      await gotoGallery(page);
      const input = page.getByRole('combobox', { name: 'Folder', exact: true });
      await input.tap();
      await page.keyboard.type('bi');
      await page.getByRole('listbox', { name: 'Folder', exact: true }).getByRole('option', { name: 'Bills' }).tap();
      await expect(input).toHaveValue('Bills');
      await input.fill('');
      await page.keyboard.type('h');
      await expect(page.getByRole('listbox', { name: 'Folder', exact: true })).toBeVisible();
      await page.getByRole('heading', { name: 'Comboboxes' }).tap();
      await expect(page.getByRole('listbox', { name: 'Folder', exact: true })).toBeHidden();
    });
  });
});

test.describe('tabs', () => {
  test.use({ viewport: { width: desktop.width, height: desktop.height } });

  test('the keyboard walk follows the tabs pattern', async ({ page }) => {
    await gotoGallery(page);
    const list = page.getByRole('tablist', { name: 'Knowledge' });
    const tab = (name: string) => list.getByRole('tab', { name });
    const panel = page.getByRole('tabpanel');
    await expect(tab('Page')).toHaveAttribute('aria-selected', 'true');
    await expect(tab('Facts')).toHaveAttribute('tabindex', '-1');
    await tab('Page').focus();
    await page.keyboard.press('ArrowRight');
    await expect(tab('Facts')).toBeFocused();
    await expect(tab('Facts')).toHaveAttribute('aria-selected', 'true');
    await expect(panel.first()).toContainText('Twelve facts');
    await page.keyboard.press('ArrowLeft');
    await page.keyboard.press('ArrowLeft');
    await expect(tab('About me')).toBeFocused();
    await expect(tab('About me')).toHaveAttribute('aria-selected', 'true');
    await page.keyboard.press('Home');
    await expect(tab('Page')).toHaveAttribute('aria-selected', 'true');
    await page.keyboard.press('End');
    await expect(tab('About me')).toBeFocused();
    const current = page.getByRole('tabpanel', { name: 'About me' });
    await expect(current).toContainText('Prefers morning reminders');
    await page.keyboard.press(keys(page).tab);
    await expect(current.getByRole('button', { name: 'Open about' })).toBeFocused();
    await page.keyboard.press(keys(page).back);
    await expect(tab('About me')).toBeFocused();

    const settings = page.getByRole('tablist', { name: 'Settings sections' });
    await settings.getByRole('tab', { name: 'General' }).focus();
    await page.keyboard.press('End');
    await expect(settings.getByRole('tab', { name: 'Apps' })).toBeFocused();
    await expect(settings.getByRole('tab', { name: 'Backups' })).toHaveAttribute('aria-disabled', 'true');
    await page.keyboard.press('ArrowRight');
    await expect(settings.getByRole('tab', { name: 'General' })).toBeFocused();
  });

  test('a pointer selects a tab and a disabled tab stays unselected', async ({ page }) => {
    await gotoGallery(page);
    const list = page.getByRole('tablist', { name: 'Knowledge' });
    await list.getByRole('tab', { name: 'Review' }).click();
    await expect(list.getByRole('tab', { name: 'Review' })).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByRole('tabpanel', { name: 'Review' })).toContainText('Three changes');
    const settings = page.getByRole('tablist', { name: 'Settings sections' });
    await settings.getByRole('tab', { name: 'Backups' }).click({ force: true });
    await expect(settings.getByRole('tab', { name: 'General' })).toHaveAttribute('aria-selected', 'true');
  });

  test.describe('touch', () => {
    test.use({ viewport: { width: 360, height: 740 }, hasTouch: true });

    test('the tab list scrolls inside itself on a phone', async ({ page }) => {
      await gotoGallery(page);
      const settings = page.getByRole('tablist', { name: 'Settings sections' });
      const overflow = await settings.evaluate((el) => el.scrollWidth > el.clientWidth);
      expect(overflow, 'the settings tab list is wider than a small phone').toBe(true);
      await expectNoHorizontalScroll(page);
      await settings.getByRole('tab', { name: 'General' }).focus();
      await page.keyboard.press('End');
      const apps = settings.getByRole('tab', { name: 'Apps' });
      await expect(apps).toBeInViewport({ ratio: 1 });
      await settings.getByRole('tab', { name: 'Health' }).tap();
      await expect(settings.getByRole('tab', { name: 'Health' })).toHaveAttribute('aria-selected', 'true');
      await expect(page.getByRole('tabpanel', { name: 'Health' })).toContainText('Settings for health.');
      await expectNoHorizontalScroll(page);
    });
  });
});

test.describe('listbox', () => {
  test.use({ viewport: { width: desktop.width, height: desktop.height } });

  test('the keyboard walk follows the single and multiple listbox patterns', async ({ page }) => {
    await gotoGallery(page);
    const single = page.getByRole('listbox', { name: 'Default folder' });
    const multi = page.getByRole('listbox', { name: 'Notify me about' });
    const result = page.getByTestId('listbox-result');
    await page.locator('[data-gallery="listboxes"] h2').evaluate((el) => {
      (el as HTMLElement).tabIndex = -1;
      (el as HTMLElement).focus();
    });
    await page.keyboard.press(keys(page).tab);
    await expect(single.getByRole('option', { name: 'Receipts' })).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await expect(single.getByRole('option', { name: 'Travel' })).toBeFocused();
    await expect(single.getByRole('option', { name: 'Receipts' })).toHaveAttribute('aria-selected', 'true');
    await page.keyboard.press('Enter');
    await expect(single.getByRole('option', { name: 'Travel' })).toHaveAttribute('aria-selected', 'true');
    await expect(single.getByRole('option', { name: 'Receipts' })).toHaveAttribute('aria-selected', 'false');
    await page.keyboard.press('Home');
    await expect(single.getByRole('option', { name: 'Inbox' })).toBeFocused();
    await page.keyboard.press('End');
    await expect(single.getByRole('option', { name: 'School' })).toBeFocused();
    await page.keyboard.press('g');
    await expect(single.getByRole('option', { name: 'Garden club' })).toBeFocused();
    await page.keyboard.press(' ');
    await expect(result).toContainText('Default garden.');

    await expect(multi).toHaveAttribute('aria-multiselectable', 'true');
    await page.keyboard.press(keys(page).tab);
    await expect(multi.getByRole('option', { name: 'Mail decisions' })).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press(' ');
    await page.keyboard.press('Shift+ArrowDown');
    await expect(multi.getByRole('option', { name: 'Finished downloads' })).toBeFocused();
    await expect(result).toContainText('Notify decisions, calendar, downloads.');
    await page.keyboard.press('Enter');
    await expect(result).toContainText('Notify decisions, calendar.');
    await page.keyboard.press('ControlOrMeta+a');
    await expect(result).toContainText('Notify decisions, calendar, downloads, health.');
    await page.keyboard.press('ControlOrMeta+a');
    await expect(result).toContainText('Notify none.');
  });

  test('a listbox in a sheet picks, closes and returns focus', async ({ page }) => {
    await gotoGallery(page);
    const opener = page.getByRole('button', { name: 'Move to folder' });
    await opener.focus();
    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog', { name: 'Move to folder' });
    const list = dialog.getByRole('listbox', { name: 'Folder', exact: true });
    await expect(list).toBeVisible();
    await list.getByRole('option', { name: 'Inbox' }).focus();
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Enter');
    await expect(dialog).toBeHidden();
    await expect(opener).toBeFocused();
    await expect(page.getByTestId('listbox-result')).toContainText('Moved to receipts.');

    await page.keyboard.press(' ');
    await expect(dialog).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(opener).toBeFocused();
  });

  test('a pointer picks and toggles options', async ({ page }) => {
    await gotoGallery(page);
    await page.getByRole('listbox', { name: 'Default folder' }).getByRole('option', { name: 'Bills' }).click();
    const multi = page.getByRole('listbox', { name: 'Notify me about' });
    await multi.getByRole('option', { name: 'Server health' }).click();
    await multi.getByRole('option', { name: 'Mail decisions' }).click();
    await page
      .getByRole('listbox', { name: 'Default folder' })
      .getByRole('option', { name: 'Archive' })
      .click({ force: true });
    await expect(page.getByTestId('listbox-result')).toContainText('Default bills. Notify health.');
  });

  test.describe('touch', () => {
    test.use({ viewport: { width: phone.width, height: phone.height }, hasTouch: true });

    test('a tap in the sheet picks and closes it', async ({ page }) => {
      await gotoGallery(page);
      await page.getByRole('button', { name: 'Move to folder' }).tap();
      const dialog = page.getByRole('dialog', { name: 'Move to folder' });
      await dialog.getByRole('option', { name: 'Travel' }).tap();
      await expect(dialog).toBeHidden();
      await expect(page.getByTestId('listbox-result')).toContainText('Moved to travel.');
    });
  });
});

const edgeCases = [
  {
    name: 'menu at the start of a row',
    target: (page: Page) => page.getByRole('button', { name: 'Thread actions' }),
    popup: (page: Page) => page.getByRole('menu', { name: 'Thread actions' }),
    open: (page: Page) => page.keyboard.press('Enter'),
  },
  {
    name: 'overflow menu at the end of a row',
    target: (page: Page) => page.getByRole('button', { name: 'More for Compare winter tyre quotes' }),
    popup: (page: Page) => page.getByRole('menu', { name: 'More for Compare winter tyre quotes' }),
    open: (page: Page) => page.keyboard.press('Enter'),
  },
  {
    name: 'combobox',
    target: (page: Page) => page.getByRole('combobox', { name: 'Folder', exact: true }),
    popup: (page: Page) => page.locator('.pop:popover-open'),
    open: (page: Page) => page.keyboard.press('ArrowDown'),
  },
];

for (const fallback of [false, true]) {
  test.describe(fallback ? 'placement without anchor positioning' : 'placement', () => {
    if (fallback) {
      test.beforeEach(async ({ page }) => {
        await page.addInitScript(() => {
          const supports = CSS.supports.bind(CSS);
          CSS.supports = ((...args: [string, string?]) =>
            args.join(' ').includes('anchor') || args.join(' ').includes('position-try')
              ? false
              : supports(...(args as [string]))) as typeof CSS.supports;
        });
      });
    }
    for (const layout of [phone, desktop]) {
      test.describe(layout.name, () => {
        test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });
        for (const item of edgeCases) {
          test(`the ${item.name} stays inside the viewport near each edge`, async ({ page }) => {
            await gotoGallery(page);
            const target = item.target(page);
            const popup = item.popup(page);
            for (const edge of ['top', 'bottom'] as const) {
              await placeAt(target, edge);
              await target.focus();
              await item.open(page);
              await expect(popup).toBeVisible();
              await settle(popup);
              const box = await expectInsideViewport(popup);
              const anchor = await target.evaluate((el) => el.getBoundingClientRect().toJSON());
              if (edge === 'top') expect(box.top, 'opens below the trigger').toBeGreaterThanOrEqual(anchor.bottom);
              else expect(box.bottom, 'flips above the trigger').toBeLessThanOrEqual(anchor.top);
              await page.keyboard.press('Escape');
              await expect(popup).toBeHidden();
            }
          });
        }
      });
    }
  });
}
