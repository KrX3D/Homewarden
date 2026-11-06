import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Builds straight into the Go module's embed directory.
export default defineConfig({
  plugins: [react()],
  build: { outDir: '../internal/web/dist', emptyOutDir: true },
  server: { proxy: { '/api': 'http://127.0.0.1:8080', '/healthz': 'http://127.0.0.1:8080' } },
});
