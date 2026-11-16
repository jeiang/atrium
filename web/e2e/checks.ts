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

export async function expectTouchTargets(page: Page, scope = 'body'): Promise<void> {
  const failures = await page.evaluate(
    ({ selector, size, scope }) => {
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
      const root = document.querySelector(scope);
      if (!root) return [`touch rule: ${scope} is not on the page`];
      const controls = [...root.querySelectorAll(selector)].filter(visible);
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
            if (hit && (hit === el || el.contains(hit) || (hit.contains(el) && !hit.matches(selector)))) continue;
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
    { selector: interactive, size: target, scope },
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
    const pageHeight = document.documentElement.scrollHeight;
    const pinned = (el: Element): boolean => {
      for (let node: Element | null = el; node; node = node.parentElement) {
        if (node.matches(':modal, :popover-open') || /fixed|sticky/.test(getComputedStyle(node).position)) return true;
      }
      return false;
    };
    const found: string[] = [];
    for (const el of document.querySelectorAll('body *')) {
      const style = getComputedStyle(el);
      const rect = el.getBoundingClientRect();
      if (rect.width < 2 || rect.height < 2 || style.visibility === 'hidden' || !isSurface(el, style)) continue;
      const [top, bottom] = pinned(el) ? [0, height] : [-scrollY, pageHeight - scrollY];
      for (const corner of corners) {
        if (Number.parseFloat(style.getPropertyValue(corner.prop)) > 0) continue;
        const x = rect[corner.x];
        const y = rect[corner.y];
        const where = `${corner.y}-${corner.x}`;
        if (x <= 0.5 || x >= width - 0.5 || y <= top + 0.5 || y >= bottom - 0.5) {
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
    for (const host of document.querySelectorAll('body *')) {
      const box = host.getBoundingClientRect();
      if (box.width < 2 || box.height < 2) continue;
      for (const pseudo of ['::before', '::after'] as const) {
        const style = getComputedStyle(host, pseudo);
        if (style.content === 'none' || style.content === 'normal' || style.display === 'none') continue;
        if (rounded(style) || !isSurface(host, style)) continue;
        let frame: Element | null = host;
        while (frame && frame !== document.body) {
          const frameStyle = getComputedStyle(frame);
          if (rounded(frameStyle) && frameStyle.overflow !== 'visible') break;
          frame = frame.parentElement;
        }
        if (frame && frame !== document.body) continue;
        found.push(`${describe(host)}${pseudo}: square corners and no rounded clipping frame`);
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

const colorProperties = [
  'color',
  'background-color',
  'background-image',
  'border-top-color',
  'border-right-color',
  'border-bottom-color',
  'border-left-color',
  'outline-color',
  'text-decoration-color',
  'column-rule-color',
  'caret-color',
  'accent-color',
  'fill',
  'stroke',
  'box-shadow',
  'text-shadow',
];

export async function expectNoBlue(page: Page, maxChroma = 0.02): Promise<void> {
  const failures = await page.evaluate(
    ({ properties, limit }) => {
      const canvas = document.createElement('canvas');
      canvas.width = 1;
      canvas.height = 1;
      const context = canvas.getContext('2d', { willReadFrequently: true });
      if (!context) throw new Error('no 2d canvas');
      const cache = new Map<string, { c: number; h: number; a: number }>();
      const oklch = (css: string): { c: number; h: number; a: number } => {
        const known = cache.get(css);
        if (known) return known;
        context.clearRect(0, 0, 1, 1);
        context.fillStyle = '#000';
        context.fillStyle = css;
        context.fillRect(0, 0, 1, 1);
        const [r8 = 0, g8 = 0, b8 = 0, a8 = 0] = context.getImageData(0, 0, 1, 1).data;
        const linear = (v: number): number => {
          const s = v / 255;
          return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
        };
        const [r, g, b] = [linear(r8), linear(g8), linear(b8)];
        const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
        const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
        const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
        const A = 1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s;
        const B = 0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s;
        const result = { c: Math.hypot(A, B), h: ((Math.atan2(B, A) * 180) / Math.PI + 360) % 360, a: a8 / 255 };
        cache.set(css, result);
        return result;
      };
      const pattern = /\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\((?:[^()]|\([^()]*\))*\)/g;
      const describe = (el: Element): string => {
        const id = el.id ? `#${el.id}` : '';
        const cls = [...el.classList].map((name) => `.${name}`).join('');
        const text = (el.textContent ?? '').trim().replace(/\s+/g, ' ').slice(0, 40);
        return `${el.tagName.toLowerCase()}${id}${cls}${text ? ` "${text}"` : ''}`;
      };
      const found = new Set<string>();
      const inspect = (el: Element, style: CSSStyleDeclaration, pseudo: string): void => {
        for (const property of properties) {
          const value = style.getPropertyValue(property);
          for (const [color] of value.matchAll(pattern)) {
            const { c, h, a } = oklch(color);
            if (a === 0 || c <= limit || h < 170 || h > 300) continue;
            found.add(
              `${describe(el)}${pseudo} ${property}: ${color} is blue (oklch chroma ${c.toFixed(3)}, hue ${h.toFixed(0)})`,
            );
          }
        }
      };
      for (const el of [document.documentElement, ...document.querySelectorAll('body, body *')]) {
        const style = getComputedStyle(el);
        if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') continue;
        inspect(el, style, '');
        for (const pseudo of ['::before', '::after']) {
          const pseudoStyle = getComputedStyle(el, pseudo);
          if (pseudoStyle.content !== 'none' && pseudoStyle.content !== 'normal') inspect(el, pseudoStyle, pseudo);
        }
        if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
          if (el.placeholder) inspect(el, getComputedStyle(el, '::placeholder'), '::placeholder');
        }
        if (el.matches('dialog[open], :popover-open')) inspect(el, getComputedStyle(el, '::backdrop'), '::backdrop');
      }
      return [...found];
    },
    { properties: colorProperties, limit: maxChroma },
  );
  expect(failures, `no blue: no painted color with hue 170 to 300 and chroma above ${maxChroma}`).toEqual([]);
}

const movement = [
  'transform',
  'translate',
  'scale',
  'rotate',
  'backgroundPosition',
  'background-position',
  'offsetDistance',
  'offset-distance',
  'inset',
  'top',
  'left',
  'right',
  'bottom',
  'margin',
  'width',
  'height',
  'all',
];

export async function expectReducedMotion(page: Page, limitMs = 380): Promise<void> {
  const failures = await page.evaluate(
    ({ moving, limit }) => {
      const describe = (target: Element | null): string => {
        if (!target) return 'document';
        const cls = [...target.classList].map((name) => `.${name}`).join('');
        return `${target.tagName.toLowerCase()}${cls}`;
      };
      const seconds = (value: string): number[] =>
        value.split(',').map((part) => {
          const text = part.trim();
          return text.endsWith('ms') ? Number.parseFloat(text) : Number.parseFloat(text) * 1000;
        });
      const found: string[] = [];
      for (const animation of document.getAnimations()) {
        const effect = animation.effect;
        if (!(effect instanceof KeyframeEffect)) continue;
        const name = `${describe(effect.target)}${effect.pseudoElement ?? ''} ${
          'animationName' in animation ? animation.animationName : 'transition'
        }`;
        const timing = effect.getComputedTiming();
        const props = new Set(effect.getKeyframes().flatMap((frame) => Object.keys(frame)));
        for (const prop of props) {
          if (moving.includes(prop)) found.push(`${name}: animates ${prop} under reduced motion`);
        }
        if (timing.iterations === Number.POSITIVE_INFINITY) found.push(`${name}: repeats forever under reduced motion`);
        if (Number(timing.duration) > limit) found.push(`${name}: lasts ${timing.duration} ms, over ${limit} ms`);
      }
      for (const el of document.querySelectorAll('body *')) {
        const style = getComputedStyle(el);
        const durations = seconds(style.transitionDuration);
        if (durations.every((ms) => ms === 0)) continue;
        const props = style.transitionProperty.split(',').map((part) => part.trim());
        for (const [index, prop] of props.entries()) {
          const ms = durations[index % durations.length] ?? 0;
          if (ms === 0) continue;
          if (moving.includes(prop)) found.push(`${describe(el)}: transitions ${prop} under reduced motion`);
          if (ms > limit) found.push(`${describe(el)}: transition of ${prop} lasts ${ms} ms, over ${limit} ms`);
        }
      }
      return [...new Set(found)];
    },
    { moving: movement, limit: limitMs },
  );
  expect(failures, 'reduced motion: no movement, only opacity, nothing longer than the slowest step').toEqual([]);
}

export async function expectFocusVisible(page: Page, scope: string): Promise<number> {
  const key = page.context().browser()?.browserType().name() === 'webkit' ? 'Alt+Tab' : 'Tab';
  await page.evaluate((selector) => {
    const root = document.querySelector(selector);
    if (!root) throw new Error(`${selector} is not on the page`);
    document.getElementById('focus-start')?.remove();
    const start = document.createElement('span');
    start.id = 'focus-start';
    start.tabIndex = -1;
    root.before(start);
    start.focus();
  }, scope);
  const failures: string[] = [];
  let stops = 0;
  for (let step = 0; step < 80; step++) {
    await page.keyboard.press(key);
    const stop = await page.evaluate((selector) => {
      const root = document.querySelector(selector);
      const el = document.activeElement;
      if (!root || !el || !root.contains(el)) return undefined;
      const canvas = document.createElement('canvas');
      canvas.width = 1;
      canvas.height = 1;
      const context = canvas.getContext('2d', { willReadFrequently: true });
      if (!context) throw new Error('no 2d canvas');
      const rgba = (css: string): number[] => {
        context.clearRect(0, 0, 1, 1);
        context.fillStyle = css;
        context.fillRect(0, 0, 1, 1);
        return [...context.getImageData(0, 0, 1, 1).data];
      };
      const luminance = (css: string): number => {
        const [r = 0, g = 0, b = 0] = rgba(css).map((v) => {
          const s = v / 255;
          return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
        });
        return 0.2126 * r + 0.7152 * g + 0.0722 * b;
      };
      const shown = (node: Element): boolean => {
        for (let n: Element | null = node; n; n = n.parentElement)
          if (getComputedStyle(n).opacity === '0') return false;
        return true;
      };
      const ring = shown(el) ? el : (el.closest('label') ?? el);
      const style = getComputedStyle(ring);
      const cls = [...ring.classList].map((name) => `.${name}`).join('');
      const text = (ring.textContent ?? '').trim().replace(/\s+/g, ' ').slice(0, 30);
      const name = `${ring.tagName.toLowerCase()}${cls}${text ? ` "${text}"` : ''}`;
      const width = Number.parseFloat(style.outlineWidth);
      if (style.outlineStyle === 'none' || !(width >= 2)) {
        return { name, problem: `no focus ring (outline ${style.outlineStyle} ${style.outlineWidth})` };
      }
      let backdrop = getComputedStyle(document.body).backgroundColor;
      const inside = Number.parseFloat(style.outlineOffset) < 0;
      for (let n: Element | null = inside ? ring : ring.parentElement; n; n = n.parentElement) {
        const bg = getComputedStyle(n).backgroundColor;
        if ((rgba(bg)[3] ?? 0) === 255) {
          backdrop = bg;
          break;
        }
      }
      const [hi, lo] = [luminance(style.outlineColor), luminance(backdrop)].sort((a, b) => b - a);
      const ratio = ((hi ?? 0) + 0.05) / ((lo ?? 0) + 0.05);
      if (ratio < 3) return { name, problem: `focus ring contrast ${ratio.toFixed(2)}:1 against ${backdrop}` };
      return { name, problem: '' };
    }, scope);
    if (!stop) break;
    stops++;
    if (stop.problem) failures.push(`${stop.name}: ${stop.problem}`);
  }
  expect(failures, `every focus stop in ${scope} shows a 2 px ring with 3:1 contrast`).toEqual([]);
  expect(stops, `${scope} has keyboard focus stops`).toBeGreaterThan(0);
  return stops;
}
