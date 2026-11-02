import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export default defineConfig({
  plugins: [svelte()],
  build: { outDir: 'dist', emptyOutDir: true, target: 'es2022', assetsInlineLimit: 0 },
  server: {
    // `npm run dev` proxies to a running duetto admin listener.
    proxy: {
      '/api': 'http://127.0.0.1:7790',
      '/media': 'http://127.0.0.1:7790',
      '/subs': 'http://127.0.0.1:7790',
      '/ws': { target: 'ws://127.0.0.1:7790', ws: true },
    },
  },
});
