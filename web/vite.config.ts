/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: 'dist',
    assetsDir: 'assets',
    // Everything is bundled; nothing is fetched from elsewhere (CSP: default-src 'self').
    assetsInlineLimit: 0,
  },
  server: {
    proxy: { '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false } },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    // page tests drive Radix widgets through jsdom; the first one in a file also pays for warm-up
    testTimeout: 20_000,
  },
})
