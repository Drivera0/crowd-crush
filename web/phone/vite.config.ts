import { defineConfig } from 'vite';

// Served by the Go server at /. `npm run dev:phone` proxies to it on :8080.
export default defineConfig({
  root: __dirname,
  base: '/',
  // Two pages: the phone page, and /beacons.html (the Bluetooth beacon check).
  // target: Vite 7's default (Chrome 107 / Safari 16) would ship syntax older judges' phones can't parse
  // (Galaxy S10+ on Chrome 96 / Samsung Internet 16, iPhones on iOS 15): lower it to what they run.
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: ['chrome87', 'safari14', 'firefox90', 'edge88'],
    cssTarget: ['chrome87', 'safari14', 'firefox90', 'edge88'],
    rollupOptions: { input: [`${__dirname}/index.html`, `${__dirname}/beacons.html`] },
  },
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
