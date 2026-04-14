import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Vite dev server proxies admin-api paths to the Go admin server on :9001.
// In production the UI is served by the Go binary from the same origin,
// so all admin calls remain relative. Only /api/* (admin surface) and
// /health (operator probe) forward to the backend; every other path
// stays with Vite so the React SPA handles client-side routing.
export default defineConfig({
  plugins: [react()],
  base: '/',
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:9001',
      '/health': 'http://localhost:9001',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
});
