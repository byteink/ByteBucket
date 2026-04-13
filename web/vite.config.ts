import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Vite dev server proxies admin-api paths to the Go admin server on :9001.
// In production the UI is served by the Go binary from the same origin,
// so all admin calls remain relative.
export default defineConfig({
  plugins: [react()],
  base: '/',
  server: {
    port: 5173,
    proxy: {
      '/users': 'http://localhost:9001',
      '/cors': 'http://localhost:9001',
      '/health': 'http://localhost:9001',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
});
