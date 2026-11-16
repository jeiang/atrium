import { defineConfig } from 'orval';

export default defineConfig({
  atrium: {
    input: { target: '../docs/api/openapi.json' },
    output: {
      mode: 'tags-split',
      target: 'src/contract/gen/endpoints',
      schemas: 'src/contract/gen/model',
      client: 'fetch',
      httpClient: 'fetch',
      clean: true,
      override: {
        mutator: { path: 'src/contract/fetcher.ts', name: 'atriumFetch' },
        operations: {
          'media.filing-preview': { operationName: () => ['mediaFilingPreview', 'mediaFilingPreviewQuery'] },
        },
      },
    },
  },
});
