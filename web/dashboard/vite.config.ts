import { defineConfig } from 'vite';

// Served by the Go server at /dash/. `npm run dev:dash` proxies to it on :8080.
export default defineConfig({
  root: __dirname,
  base: '/dash/',
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    port: 5174,
    fs: { allow: ['..'] },
    proxy: {
      '/api': 'http://localhost:8080',
      '/audio': 'http://localhost:8080',
      '/ws': { target: 'ws://localhost:8080', ws: true },
    },
  },
});
