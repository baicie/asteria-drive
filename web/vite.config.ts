import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

/**
 * When ASTERIA_E2E_API is set (for example http://127.0.0.1:18091), `vite preview`
 * forwards /api to a real control plane, mirroring the nginx /api/ proxy used by the
 * container image. Playwright can then exercise the real PostgreSQL + S3 stack through
 * the same same-origin path a browser uses in production.
 */
const previewApiTarget = process.env.ASTERIA_E2E_API

export default defineConfig({
  plugins: [
    vue({
      template: {
        compilerOptions: {
          isCustomElement: (tag) => tag.startsWith('zw-'),
        },
      },
    }),
  ],
  server: {
    host: '0.0.0.0',
    port: 5173,
  },
  preview: previewApiTarget
    ? { proxy: { '/api': { target: previewApiTarget, changeOrigin: true } } }
    : undefined,
  resolve: {
    dedupe: ['vue'],
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
    // Playwright owns ./e2e; Vitest must never pick up those specs.
    exclude: ['e2e/**', 'node_modules/**'],
    restoreMocks: true,
    coverage: {
      provider: 'v8',
      // text-summary for the console, lcov + json-summary so CI keeps the numbers
      // as failure evidence instead of only printing them into a log.
      reporter: ['text-summary', 'lcov', 'json-summary'],
      include: ['src/**/*.ts'],
      exclude: ['src/**/*.test.ts', 'src/main.ts', 'src/env.d.ts', 'src/shims-vue.d.ts'],
    },
  },
})
