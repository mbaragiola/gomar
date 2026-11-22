import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { posShell } from './pos-shell-build'
import { themeBootstrap } from './theme-bootstrap'

export default defineConfig({
  plugins: [
    vue(),
    posShell(),
    {
      name: 'gomar-first-paint',
      transformIndexHtml: {
        order: 'pre',
        handler: () => [{ tag: 'script', children: themeBootstrap, injectTo: 'head-prepend' }],
      },
    },
  ],
  base: '/static/',
  build: {
    outDir: '../var/static',
    emptyOutDir: true,
    manifest: true,
    rollupOptions: {
      output: {
        entryFileNames: 'app.js',
        assetFileNames: (asset) => asset.names.some((name) => name.endsWith('.css')) ? 'style.css' : 'assets/[name][extname]',
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
  },
})
