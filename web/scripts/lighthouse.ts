import { once } from 'node:events';
import { readFileSync } from 'node:fs';
import type { AddressInfo } from 'node:net';
import { createServer } from 'node:net';
import lighthouse from 'lighthouse';
import type { Result } from 'lighthouse/types/lh.js';
import { chromium } from 'playwright-core';
import { fakeApi, signedInApi } from '../e2e/api.ts';
import { serve } from '../e2e/server.ts';

interface Budgets {
  performance: number;
  accessibility: number;
  bestPractices: number;
  lcpMs: number;
  tbtMs: number;
  cls: number;
  otherOrigins: number;
}

interface Measure {
  name: string;
  key: keyof Budgets;
  floor: boolean;
  value: (lhr: Result) => number;
}

const budgetFile = 'web/budgets.json';
const runs = 5;
const budgets: Budgets = JSON.parse(readFileSync(new URL('../budgets.json', import.meta.url), 'utf8')).lighthouse;
const dist = process.argv[2] ?? 'dist';
const base = '/';

const score = (category: string) => (lhr: Result) => lhr.categories[category]?.score ?? 0;
const metric = (audit: string) => (lhr: Result) => lhr.audits[audit]?.numericValue ?? Number.POSITIVE_INFINITY;
const measures: Measure[] = [
  { name: 'performance score', key: 'performance', floor: true, value: score('performance') },
  { name: 'accessibility score', key: 'accessibility', floor: true, value: score('accessibility') },
  { name: 'best practices score', key: 'bestPractices', floor: true, value: score('best-practices') },
  { name: 'LCP ms', key: 'lcpMs', floor: false, value: metric('largest-contentful-paint') },
  { name: 'TBT ms', key: 'tbtMs', floor: false, value: metric('total-blocking-time') },
  { name: 'CLS', key: 'cls', floor: false, value: metric('cumulative-layout-shift') },
  {
    name: 'other-origin requests',
    key: 'otherOrigins',
    floor: false,
    value: (lhr) => {
      const details = lhr.audits['network-requests']?.details;
      const items = details?.type === 'table' ? details.items : [];
      return items.filter((item) => new URL(String(item.url)).origin !== new URL(lhr.finalDisplayedUrl).origin).length;
    },
  },
];

const probe = createServer();
await once(probe.listen(0, '127.0.0.1'), 'listening');
const { port } = probe.address() as AddressInfo;
probe.close();

const { url, server } = await serve(dist, { brotli: true, base, handle: fakeApi(signedInApi()) });
const browser = await chromium.launch({ channel: 'chromium', args: [`--remote-debugging-port=${port}`] });
const results: Result[] = [];
try {
  for (let run = 0; run < runs; run++) {
    const outcome = await lighthouse(`${url}${base}`, {
      port,
      logLevel: 'error',
      output: 'json',
      throttlingMethod: 'simulate',
      onlyCategories: ['performance', 'accessibility', 'best-practices'],
    });
    if (!outcome) throw new Error('lighthouse returned no result');
    if (outcome.lhr.runtimeError) throw new Error(`lighthouse: ${outcome.lhr.runtimeError.message}`);
    results.push(outcome.lhr);
  }
} finally {
  await browser.close();
  server.close();
}

const median = (values: number[]): number => values.toSorted((a, b) => a - b)[Math.floor(values.length / 2)] ?? 0;
const failures: string[] = [];
console.log(
  `web-lighthouse: ${dist} at ${base} signed in on Home, brotli served, Lighthouse ${results[0]?.lighthouseVersion} mobile preset with simulated throttling, median of ${runs}`,
);
console.log(`${'item'.padEnd(22)}  ${'median'.padStart(8)}  ${'budget'.padStart(8)}  runs`);
for (const measure of measures) {
  const values = results.map(measure.value);
  const value = median(values);
  const budget = budgets[measure.key];
  const shown = (n: number): string => (Number.isInteger(n) || n > 10 ? n.toFixed(0) : n.toFixed(3));
  console.log(
    `${measure.name.padEnd(22)}  ${shown(value).padStart(8)}  ${`${measure.floor ? '>=' : '<='}${budget}`.padStart(8)}  ${values.map(shown).join(' ')}`,
  );
  if (measure.floor ? !(value >= budget) : !(value <= budget)) {
    failures.push(
      `web-lighthouse: ${measure.name} is ${shown(value)}, ${measure.floor ? 'below' : 'over'} the budget "lighthouse.${measure.key}" of ${budget} in ${budgetFile}`,
    );
  }
}
for (const failure of failures) console.error(failure);
process.exit(failures.length > 0 ? 1 : 0);
