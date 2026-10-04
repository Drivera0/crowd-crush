import { defineConfig } from 'vite';

// Served by the Go server at /. `npm run dev:phone` proxies to it on :8080.
export default defineConfig({
  root: __dirname,
  base: '/',
  // Two pages: the phone page, and /beacons.html (the Bluetooth beacon check).
  build: { outDir: 'dist', emptyOutDir: true, rollupOptions: { input: [`${__dirname}/index.html`, `${__dirname}/beacons.html`] } },
  server: {
    port: 5173,
    host: true,
    fs: { allow: ['..'] },
    proxy: {
      '/api': 'http://localhost:8080',
      '/ws': { target: 'ws://localhost:8080', ws: true },
    },
  },
});
