import { defineConfig } from 'vitest/config';
import { fileURLToPath } from 'node:url';

export default defineConfig({
  // the Console imports the CLI's goldens with ?raw from ../examples (snippets.ts); DOM tests that
  // load a controller reach them through components.ts
  server: { fs: { allow: [fileURLToPath(new URL('..', import.meta.url))] } },
  resolve: { alias: { '@verify': fileURLToPath(new URL('./verify/src', import.meta.url)) } },
  test: {
    include: ['test/**/*.test.ts'],
    environment: 'node',
    testTimeout: 30_000,
  },
});
