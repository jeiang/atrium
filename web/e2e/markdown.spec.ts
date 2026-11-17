import { readFileSync } from 'node:fs';
import type { Page } from '@playwright/test';
import { gotoShell } from './checks.ts';
import { expect, test } from './fixtures.ts';

interface Example {
  set: 'commonmark' | 'gfm';
  section: string;
  n: number;
  markdown: string;
  html: string;
  raw?: boolean;
}

const { examples }: { examples: Example[] } = JSON.parse(
  readFileSync(new URL('./markdown-examples.json', import.meta.url), 'utf8'),
);

const scheme = 'the policy links only http, https, mailto and same-app paths; other schemes render as text';
const literal =
  'the policy enables GFM autolink literals, which link bare URLs and emails that plain CommonMark leaves as text';

const deviations: Record<string, string> = {
  'commonmark 596': scheme,
  'commonmark 598': scheme,
  'commonmark 599': scheme,
  'commonmark 601': scheme,
  'commonmark 602': literal,
  'commonmark 606': literal,
  'commonmark 608': literal,
  'commonmark 611': literal,
  'commonmark 612': literal,
  'gfm 18': scheme,
};

const allowedTags =
  'section p h3 h4 h5 h6 div span pre code blockquote ul ol li hr br table thead tbody tr th td strong em del a svg path';
const allowedAttributes = 'class href title target rel start role aria-label aria-hidden tabindex viewBox focusable d';

const hostile = [
  '<script>window.atriumXss = 1</script>',
  '<img src=x onerror="window.atriumXss = 1">',
  '<svg onload="window.atriumXss = 1"></svg>',
  '<iframe src="https://evil.example/" srcdoc="<script>window.atriumXss = 1</script>"></iframe>',
  '<a href="javascript:window.atriumXss = 1">raw anchor</a>',
  '<a href="https://example.com/" onclick="window.atriumXss = 1">raw anchor</a>',
  '<div style="background: url(https://evil.example/pixel)">styled</div>',
  '<style>body { display: none }</style>',
  '<form action="https://evil.example/"><button>Go</button></form>',
  '<math><mi xlink:href="javascript:window.atriumXss = 1">x</mi></math>',
  '<<script>script>window.atriumXss = 1<</script>/script>',
  '<scr<script>ipt>window.atriumXss = 1</scr</script>ipt>',
  '<img src="x" onerror="window.atriumXss = 1"',
  '<!-- <img src=x onerror="window.atriumXss = 1"> -->',
  '<![CDATA[<img src=x onerror="window.atriumXss = 1">]]>',
  '[js](javascript:window.atriumXss=1)',
  '[js](JaVaScRiPt:window.atriumXss=1)',
  '[js](&#106;avascript:window.atriumXss=1)',
  '[js](java&#x09;script:window.atriumXss=1)',
  '[js](<javascript:window.atriumXss=1>)',
  '[js](%6Aavascript:window.atriumXss=1)',
  '[vb](vbscript:msgbox(1))',
  '[data](data:text/html;base64,PHNjcmlwdD53aW5kb3cuYXRyaXVtWHNzPTE8L3NjcmlwdD4=)',
  '[file](file:///etc/passwd)',
  '<javascript:window.atriumXss=1>',
  '[ref]\n\n[ref]: javascript:window.atriumXss=1',
  '![x](javascript:window.atriumXss=1)',
  '![tracker](https://evil.example/pixel.png)',
  '![ref][pixel]\n\n[pixel]: https://evil.example/pixel.png',
  '[t](https://example.com/ "x\\" onmouseover=\\"window.atriumXss=1")',
  "[t](https://example.com/ '\"><img src=x onerror=window.atriumXss=1>')",
  '&lt;script&gt;window.atriumXss = 1&lt;/script&gt;',
  '&#60;img src=x onerror=window.atriumXss=1&#62;',
  '&amp;lt;script&amp;gt;',
  '`<img src=x onerror=window.atriumXss=1>`',
  '```html\n<script>window.atriumXss = 1</script>\n```',
  '| <img src=x onerror=window.atriumXss=1> | b |\n| --- | --- |\n| <script>window.atriumXss=1</script> | c |',
  'www.example.com/<script>window.atriumXss=1</script>',
  'https://example.com/"onmouseover="window.atriumXss=1',
  'mailto:sam@example.test?body=<script>',
];

async function gotoLab(page: Page): Promise<void> {
  await gotoShell(page, '/__gallery');
  await expect(page.locator('[data-gallery="prose"] .md-h').first()).toHaveText('Winter tyre quotes', { timeout: 0 });
}

async function renderEach(page: Page, sources: string[]): Promise<string[]> {
  const field = await page.getByRole('textbox', { name: 'Markdown source' }).elementHandle();
  return page.evaluate(
    async ({ field, sources }) => {
      const input = field as HTMLTextAreaElement;
      const output = document.querySelector('[data-gallery="markdown-lab"] .prose') as HTMLElement;
      const canonical = (): string => {
        const copy = output.cloneNode(true) as HTMLElement;
        for (const el of copy.querySelectorAll('.md-lang, svg, .visually-hidden')) el.remove();
        for (const el of copy.querySelectorAll('.md-code, .md-table, p.md-raw'))
          el.replaceWith(...el.childNodes, el.matches('p') ? '\n' : '');
        for (const el of copy.querySelectorAll('*')) {
          const align = /md-(left|center|right)/.exec(el.className)?.[1];
          for (const name of ['class', 'target', 'rel', 'role', 'aria-label', 'tabindex']) el.removeAttribute(name);
          if (align) el.setAttribute('align', align);
        }
        return copy.innerHTML;
      };
      const results: string[] = [];
      for (const source of sources) {
        input.value = source;
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await new Promise((resolve) => setTimeout(resolve, 0));
        results.push(canonical());
      }
      return results;
    },
    { field, sources },
  );
}

function normalize(html: string): string {
  return html
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/<(br|hr|img)([^>]*?)\s*\/>/g, '<$1$2>')
    .replace(/&nbsp;/g, '\u00a0')
    .replace(/&quot;/g, '"')
    .replace(/&gt;/g, '>')
    .replace(/&#(\d+);/g, (_, code) => String.fromCodePoint(Number(code)))
    .replace(/\s+/g, ' ')
    .replace(/ ?(<\/?(?:p|h\d|ul|ol|li|pre|blockquote|table|thead|tbody|tr|th|td|hr)\b[^>]*>) ?/g, '$1')
    .replace(/\s+<\/code><\/pre>/g, '</code></pre>')
    .replace(/<br> /g, '<br>')
    .trim();
}

function expected(example: Example): string {
  return example.html
    .replace(/<(\/?)h([1-6])>/g, (_, slash, level) => `<${slash}h${Math.min(Number(level) + 2, 6)}>`)
    .replace(/<code class="language-[^"]*">/g, '<code>')
    .replace(
      /<img src="([^"]*)" alt="([^"]*)"( title="[^"]*")? \/>/g,
      (_, src, alt, title = '') => `<a href="${src}"${title}>${alt || src}</a>`,
    );
}

test.describe('markdown policy', () => {
  test('CommonMark and the GFM table, strikethrough and autolink examples render to the policy', async ({ page }) => {
    test.slow();
    await gotoLab(page);
    const rendered = await renderEach(
      page,
      examples.map((example) => example.markdown),
    );
    const sections = new Map<string, [number, number]>();
    const failing: Record<string, string> = {};
    examples.forEach((example, index) => {
      const key = `${example.set}: ${example.section}`;
      const count = sections.get(key) ?? [0, 0];
      count[1]++;
      if (normalize(rendered[index] ?? '') === normalize(expected(example))) count[0]++;
      else failing[`${example.set} ${example.n}`] = `${rendered[index]} instead of ${expected(example)}`;
      sections.set(key, count);
    });
    const summary = [...sections].map(([key, [pass, total]]) => `${key} ${pass}/${total}`).join('; ');
    test.info().annotations.push({ type: 'conformance', description: summary });
    const unexpected = Object.entries(failing).filter(([key]) => !(key in deviations));
    expect(unexpected, 'examples that render differently from the reference').toEqual([]);
    expect(Object.keys(failing).sort(), 'listed deviations that now match the reference').toEqual(
      Object.keys(deviations).sort(),
    );
  });
});

test.describe('markdown safety', () => {
  test('hostile input executes nothing and leaves only allowed elements, attributes and links', async ({ page }) => {
    const thirdParty: string[] = [];
    page.on('request', (request) => {
      if (new URL(request.url()).hostname.endsWith('evil.example')) thirdParty.push(request.url());
    });
    await gotoLab(page);
    const field = await page.getByRole('textbox', { name: 'Markdown source' }).elementHandle();
    const findings = await page.evaluate(
      async ({ field, hostile, tags, attributes }) => {
        const input = field as HTMLTextAreaElement;
        const output = document.querySelector('[data-gallery="markdown-lab"] .prose') as HTMLElement;
        const allowedTags = tags.split(' ');
        const allowedAttributes = attributes.split(' ');
        const found: string[] = [];
        for (const source of hostile) {
          input.value = source;
          input.dispatchEvent(new Event('input', { bubbles: true }));
          await new Promise((resolve) => setTimeout(resolve, 50));
          for (const el of output.querySelectorAll('*')) {
            if (!allowedTags.includes(el.tagName.toLowerCase())) found.push(`${source}: element ${el.tagName}`);
            for (const { name, value } of el.attributes) {
              if (!allowedAttributes.includes(name)) found.push(`${source}: attribute ${name}="${value}"`);
            }
            if (el instanceof HTMLAnchorElement) {
              const url = new URL(el.href);
              const same = url.origin === location.origin;
              if (!same && !['http:', 'https:', 'mailto:'].includes(url.protocol))
                found.push(`${source}: link ${el.href}`);
              if (url.protocol.startsWith('http') && !same && el.rel !== 'noopener noreferrer') {
                found.push(`${source}: external link without rel`);
              }
            }
          }
        }
        if (Reflect.get(window, 'atriumXss') !== undefined) found.push('a payload ran');
        return found;
      },
      { field, hostile, tags: allowedTags, attributes: allowedAttributes },
    );
    expect(findings).toEqual([]);
    expect(thirdParty, 'requests to a third party').toEqual([]);
    await page.getByRole('textbox', { name: 'Markdown source' }).fill(hostile[0] ?? '');
    await expect(page.locator('[data-gallery="markdown-lab"]')).toContainText('<script>window.atriumXss = 1</script>');
  });

  test('links open safely, images stay links and nothing loads from another site', async ({ page }) => {
    const remote: string[] = [];
    page.on('request', (request) => {
      if (new URL(request.url()).hostname.endsWith('example.com')) remote.push(request.url());
    });
    await gotoLab(page);
    const sample = page.locator('[data-gallery="prose"]').first();
    const external = sample.getByRole('link', { name: 'https://example.com/quotes/winter-tyres' });
    await expect(external).toHaveAttribute('target', '_blank');
    await expect(external).toHaveAttribute('rel', 'noopener noreferrer');
    await expect(external).toHaveAccessibleName(/\(new tab\)$/);
    const internal = sample.getByRole('link', { name: 'same-app link' });
    await expect(internal).toHaveAttribute('href', '/inbox');
    await expect(internal).not.toHaveAttribute('target', /./);
    await expect(sample.getByRole('link', { name: 'sam@example.test' })).toHaveAttribute(
      'href',
      'mailto:sam@example.test',
    );
    await expect(sample.getByRole('link', { name: /^Map of the garage/ })).toHaveAttribute(
      'href',
      'https://example.com/map.png',
    );
    await expect(sample.getByRole('link', { name: /^link \(new tab\)$/ })).toHaveAttribute(
      'href',
      'https://example.com/hillfield',
    );
    await expect(sample.locator('img')).toHaveCount(0);
    await expect(sample.locator('.md-lang')).toHaveText('json');
    await expect(sample.getByText('<div onclick="x()">A raw HTML block stays text.</div>')).toBeVisible();
    await expect(sample.getByRole('heading', { level: 3, name: 'Winter tyre quotes' })).toBeVisible();
    await expect(sample.getByRole('heading', { level: 4, name: 'Setext heading' })).toBeVisible();
    await expect(sample.getByRole('region', { name: 'Table' })).toHaveAttribute('tabindex', '0');
    await expect(sample).toContainText('© & © 🚗');
    expect(remote).toEqual([]);
  });

  test('streamed partial states render without errors or a jump when a block closes', async ({ page }) => {
    await gotoLab(page);
    const field = await page.getByRole('textbox', { name: 'Markdown source' }).elementHandle();
    const steps = await page.evaluate(
      async ({ field, streams }) => {
        const input = field as HTMLTextAreaElement;
        const output = document.querySelector('[data-gallery="markdown-lab"] .prose') as HTMLElement;
        const result: Record<string, { height: number; html: string }[]> = {};
        for (const [name, chunks] of Object.entries(streams)) {
          let text = '';
          result[name] = [];
          for (const chunk of chunks) {
            text += chunk;
            input.value = text;
            input.dispatchEvent(new Event('input', { bubbles: true }));
            await new Promise((resolve) => requestAnimationFrame(resolve));
            result[name].push({ height: output.getBoundingClientRect().height, html: output.innerHTML });
          }
        }
        return result;
      },
      {
        field,
        streams: {
          fence: ['Config:\n\n', '```', 'json', '\n', '{ "slot": ', '"Thursday" }', '\n', '```'],
          link: ['See ', '[the quote](', 'https://example.com/', 'quote', ')'],
          table: ['| Garage | Price |', '\n', '| --- ', '| ---: |', '\n| Hillfield | 412 |', '\n| Ashby | 438 |'],
        },
      },
    );
    const fence = steps.fence ?? [];
    expect(fence.at(-2)?.html).toContain('<pre>');
    expect(fence.at(-1)?.height).toBeCloseTo(fence.at(-2)?.height ?? 0, 0);
    const link = steps.link ?? [];
    expect(link.at(-1)?.html).toMatch(/href="https:\/\/example\.com\/quote"><!---->(<!---->)*the quote/);
    expect(link.at(-1)?.height).toBeCloseTo(link.at(-2)?.height ?? 0, 0);
    const table = (steps.table ?? []).slice(3);
    expect(table[0]?.html).toContain('<table>');
    for (const [index, step] of table.entries()) {
      expect(step.height).toBeGreaterThanOrEqual(table[index - 1]?.height ?? 0);
    }
  });
});
