// The Console (S24 §3.1): static, served by deedglass under /app, embedded from internal/console/dist.
// Nothing is inlined (no inline <script> or <style>), so the strict CSP holds; check-dist checks it.
import { defineConfig } from 'astro/config';
import tailwindcss from '@tailwindcss/vite';
import { fileURLToPath } from 'node:url';

const verify = fileURLToPath(new URL('./verify/src', import.meta.url));

export default defineConfig({
  output: 'static',
  base: '/app',
  outDir: '../internal/console/dist',
  trailingSlash: 'never',
  build: { format: 'file', inlineStylesheets: 'never', assets: '_astro' },
  devToolbar: { enabled: false },
  compressHTML: true,
  scopedStyleStrategy: 'class',
  vite: {
    plugins: [tailwindcss()],
    build: { assetsInlineLimit: 0, cssCodeSplit: false },
    resolve: { alias: { '@verify': verify } },
    server: {
      fs: { allow: [fileURLToPath(new URL('.', import.meta.url))] },
      proxy: Object.fromEntries(['/v1', '/login', '/auth', '/.well-known'].map((p) => [p, { target: 'http://127.0.0.1:8080', changeOrigin: false }])),
    },
  },
});
