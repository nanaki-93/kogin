import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import { fileURLToPath } from 'node:url';
import { readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import type { ProxyOptions } from 'vite';

// In development the page is served by Vite, so the daemon cannot write its
// token into it; the proxy reads the daemon's token file and adds the token to
// requests that change state, as the served Web UI does itself.
function daemonTokenFile(): string {
  const explicit = process.env.KOGIN_TOKEN_FILE ?? process.env.KASURI_TOKEN_FILE ?? process.env.MINI_ORCA_TOKEN_FILE;
  if (explicit) return explicit;
  const config = process.platform === 'darwin' ? join(homedir(), 'Library', 'Application Support') : process.platform === 'win32' ? (process.env.APPDATA ?? homedir()) : (process.env.XDG_CONFIG_HOME ?? join(homedir(), '.config'));
  return join(config, 'kogin', 'daemon-token');
}

const daemon: ProxyOptions = {
  target: 'http://127.0.0.1:9090',
  configure: proxy => {
    proxy.on('proxyReq', (proxyReq, req) => {
      if (req.method === 'GET' || req.method === 'HEAD') return;
      try {
        proxyReq.setHeader('X-Kogin-Token', readFileSync(daemonTokenFile(), 'utf8').trim());
      } catch {
        // Without the file the daemon refuses the request and says why.
      }
    });
  },
};

// The daemon serves the built UI at /ui and its files under /ui/assets/ with a
// strict CSP (no inline script or style), so everything is emitted as files and
// nothing is inlined. The output is embedded into the Go binary.
export default defineConfig({
  base: '/ui/assets/',
  plugins: [react()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    outDir: '../internal/api/handlers/webui/dist',
    emptyOutDir: true,
    assetsDir: '',
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
    cssCodeSplit: false,
    sourcemap: false,
    target: 'es2022',
    chunkSizeWarningLimit: 1200,
    rollupOptions: {
      output: {
        entryFileNames: 'app-[hash].js',
        chunkFileNames: 'chunk-[hash].js',
        assetFileNames: '[name]-[hash][extname]',
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': daemon,
      '/status': daemon,
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    css: false,
  },
});
