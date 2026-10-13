import path from 'node:path'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

// Builds the worker control panel into internal/webui/dist, which the Go
// binary embeds via go:embed. The base is '/' and assets are emitted flat so
// the worker can serve them from its own root.
export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  resolve: {
    alias: { $lib: path.resolve('./src/lib') },
  },
  server: {
    host: '0.0.0.0',
    port: 18500,
    strictPort: true,
    allowedHosts: true,
  },
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    target: 'es2022',
  },
})
