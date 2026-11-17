import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { placementProblems } from '../src/features/settings/placement.ts';

interface SchemaDocument {
  pages: { fields: { key: string; group: string }[] }[];
}

const path = process.argv[2] ?? fileURLToPath(new URL('../../docs/api/settings-schema.json', import.meta.url));
const document: SchemaDocument = JSON.parse(readFileSync(path, 'utf8'));
const fields = document.pages.flatMap((page) => page.fields);
const problems = placementProblems(fields);

if (problems.length > 0) {
  for (const { key, problem } of problems) console.error(`settings-placement: ${key}: ${problem}`);
  console.error(
    `settings-placement: ${problems.length} of ${fields.length} settings have no single page; add the group to web/src/features/settings/placement.ts`,
  );
  process.exit(1);
}
console.log(`settings-placement: all ${fields.length} settings have one page`);
