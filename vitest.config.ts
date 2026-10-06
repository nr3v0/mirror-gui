import { defineConfig } from 'vitest/config';

// Backend tests are Go tests (`go test ./...`); vitest only covers the Node helper scripts.
export default defineConfig({
  test: {
    environment: 'node',
    include: ['tests/**/*.test.ts'],
    exclude: ['tests/e2e/**'],
  },
});
