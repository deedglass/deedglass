import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    testTimeout: 30_000,
    coverage: {
      provider: 'v8',
      include: ['src/**/*.ts'],
      exclude: ['src/verify/**', 'src/types.ts'],
      reporter: ['text-summary', 'json-summary'],
      thresholds: { statements: 90, lines: 90 },
    },
    projects: [
      { test: { name: 'unit', include: ['test/unit/**/*.test.ts', 'test/vectors/**/*.test.ts', 'test/adapters/**/*.test.ts', 'test/runtime/**/*.test.ts'], environment: 'node', testTimeout: 30_000 } },
      { test: { name: 'integration', include: ['test/integration/**/*.test.ts'], environment: 'node', testTimeout: 120_000, fileParallelism: false } },
    ],
  },
});
