import AxeBuilder from '@axe-core/playwright';
import { expect, type Page } from '@playwright/test';

const target = 44;

const interactive = [
  'a[href]',
  'button',
  'input:not([type="hidden"])',
  'select',
  'textarea',
  'summary',
  '[tabindex]:not([tabindex="-1"])',
  '[role="button"]',
  '[role="link"]',
  '[role="tab"]',
  '[role="menuitem"]',
  '[role="option"]',
  '[role="switch"]',
  '[role="checkbox"]',
  '[role="radio"]',
].join(', ');

export async function gotoShell(page: Page, path: string): Promise<void> {
  await page.goto(path);
  await expect(page.getByText('Signed in as Operator')).toBeVisible();
  await page.evaluate(() => document.fonts.ready);
}

export async function expectNoHorizontalScroll(page: Page): Promise<void> {
  const overflow = await page.evaluate(() => {
    const root = document.documentElement;
    return { scroll: root.scrollWidth, client: root.clientWidth };
  });
  expect(
    overflow.scroll,
    `page scrolls sideways: scrollWidth ${overflow.scroll} px > clientWidth ${overflow.client} px`,
  ).toBeLessThanOrEqual(overflow.client);
}

export async function expectTouchTargets(page: Page): Promise<void> {
  const failures = await page.evaluate(
    ({ selector, size }) => {
      const describe = (el: Element): string => {
        const id = el.id ? `#${el.id}` : '';
        const cls = [...el.classList].map((name) => `.${name}`).join('');
        const href = el.getAttribute('href');
        const text = (el.textContent ?? '').trim().replace(/\s+/g, ' ').slice(0, 40);
        return `${el.tagName.toLowerCase()}${id}${cls}${href ? `[href="${href}"]` : ''}${text ? ` "${text}"` : ''}`;
      };
      const visible = (el: Element): boolean => {
        const rect = el.getBoundingClientRect();
        const style = getComputedStyle(el);
        return rect.width > 0 && rect.height > 0 && style.visibility !== 'hidden' && !el.closest('[inert]');
      };
      const half = size / 2;
      const found: string[] = [];
      const squares: { name: string; x: number; y: number }[] = [];
      const controls = [...document.querySelectorAll(selector)].filter(visible);
      for (const el of controls) {
        const name = describe(el);
        el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' });
        const rect = el.getBoundingClientRect();
        if (rect.width < size || rect.height < size) {
          found.push(`${name}: ${Math.round(rect.width)}x${Math.round(rect.height)} px, needs ${size}x${size} px`);
          continue;
        }
        const cx = rect.left + rect.width / 2;
        const cy = rect.top + rect.height / 2;
        squares.push({ name, x: cx + scrollX, y: cy + scrollY });
        for (const dx of [-half + 1, 0, half - 1]) {
          for (const dy of [-half + 1, 0, half - 1]) {
            const hit = document.elementFromPoint(cx + dx, cy + dy);
            if (hit && (hit === el || el.contains(hit))) continue;
            found.push(
              `${name}: point (${Math.round(dx)}, ${Math.round(dy)}) from its center hits ${hit ? describe(hit) : 'nothing'}`,
            );
          }
        }
      }
      scrollTo(0, 0);
      for (const [i, a] of squares.entries()) {
        for (const b of squares.slice(i + 1)) {
          if (Math.abs(a.x - b.x) < size && Math.abs(a.y - b.y) < size) {
            found.push(`${a.name} and ${b.name}: their ${size} px squares overlap`);
          }
        }
      }
      return found;
    },
    { selector: interactive, size: target },
  );
  expect(failures, `strict touch rule: every control owns a ${target}x${target} px square`).toEqual([]);
}

export async function expectRoundedSurfaces(page: Page, windowEdge: readonly string[] = []): Promise<void> {
  const failures = await page.evaluate((allowed) => {
    const describe = (el: Element): string => {
      const id = el.id ? `#${el.id}` : '';
      const cls = [...el.classList].map((name) => `.${name}`).join('');
      const text = (el.textContent ?? '').trim().replace(/\s+/g, ' ').slice(0, 40);
      return `${el.tagName.toLowerCase()}${id}${cls}${text ? ` "${text}"` : ''}`;
    };
    const painted = (color: string): boolean => !/^(transparent|rgba\(.*,\s*0\)|.* \/ 0\))$/.test(color);
    const backdrop = (el: Element): string => {
      for (let node = el.parentElement; node; node = node.parentElement) {
        const color = getComputedStyle(node).backgroundColor;
        if (painted(color)) return color;
      }
      return getComputedStyle(document.body).backgroundColor;
    };
    const tags = new Set(['IMG', 'VIDEO', 'CANVAS', 'PROGRESS', 'METER', 'KBD', 'PRE', 'BLOCKQUOTE']);
    const isSurface = (el: Element, style: CSSStyleDeclaration): boolean => {
      const sides = ['Top', 'Right', 'Bottom', 'Left'].filter(
        (side) =>
          Number.parseFloat(style.getPropertyValue(`border-${side.toLowerCase()}-width`)) > 0 &&
          style.getPropertyValue(`border-${side.toLowerCase()}-style`) !== 'none',
      );
      return (
        (painted(style.backgroundColor) && style.backgroundColor !== backdrop(el)) ||
        sides.length >= 2 ||
        (style.boxShadow !== 'none' && !style.boxShadow.includes('inset')) ||
        style.backgroundImage !== 'none' ||
        tags.has(el.tagName) ||
        (el.tagName === 'CODE' && !el.closest('pre'))
      );
    };
    const corners = [
      { prop: 'border-top-left-radius', x: 'left', y: 'top' },
      { prop: 'border-top-right-radius', x: 'right', y: 'top' },
      { prop: 'border-bottom-right-radius', x: 'right', y: 'bottom' },
      { prop: 'border-bottom-left-radius', x: 'left', y: 'bottom' },
    ] as const;
    const rounded = (style: CSSStyleDeclaration): boolean =>
      corners.some(({ prop }) => Number.parseFloat(style.getPropertyValue(prop)) > 0);
    const width = document.documentElement.clientWidth;
    const height = document.documentElement.clientHeight;
    const found: string[] = [];
    for (const el of document.querySelectorAll('body *')) {
      const style = getComputedStyle(el);
      const rect = el.getBoundingClientRect();
      if (rect.width < 2 || rect.height < 2 || style.visibility === 'hidden' || !isSurface(el, style)) continue;
      for (const corner of corners) {
        if (Number.parseFloat(style.getPropertyValue(corner.prop)) > 0) continue;
        const x = rect[corner.x];
        const y = rect[corner.y];
        const where = `${corner.y}-${corner.x}`;
        if (x <= 0.5 || x >= width - 0.5 || y <= 0.5 || y >= height - 0.5) {
          if (!allowed.some((selector) => el.matches(selector))) {
            found.push(`${describe(el)}: square ${where} corner on the window edge, not an allowed edge surface`);
          }
          continue;
        }
        let frame: Element | null = el.parentElement;
        while (frame && frame !== document.body && !rounded(getComputedStyle(frame))) frame = frame.parentElement;
        const clips = frame && frame !== document.body && getComputedStyle(frame).overflow !== 'visible';
        if (clips) continue;
        found.push(
          frame && frame !== document.body
            ? `${describe(el)}: square ${where} corner inside ${describe(frame)}, which does not clip it`
            : `${describe(el)}: square ${where} corner`,
        );
      }
    }
    return found;
  }, windowEdge);
  expect(failures, 'every bordered or filled surface has rounded corners').toEqual([]);
}

export async function expectNoSeriousAxeFindings(page: Page): Promise<void> {
  const results = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22a', 'wcag22aa'])
    .analyze();
  const findings = results.violations
    .filter((violation) => violation.impact === 'serious' || violation.impact === 'critical')
    .map(
      (violation) =>
        `${violation.id} (${violation.impact}): ${violation.help}; ${violation.nodes.map((node) => node.target.join(' ')).join(', ')}`,
    );
  expect(findings, 'axe findings with serious or critical impact').toEqual([]);
}
