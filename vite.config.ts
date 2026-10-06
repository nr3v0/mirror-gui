import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// In development, `npm run dev` serves the UI with hot reload on port 3000 and
// proxies API calls to the Go backend (`npm run dev:server`, port 3001).
const apiTarget = process.env.MIRROR_GUI_API_URL || 'http://localhost:3001';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    proxy: {
      '/api': apiTarget,
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
  },
});
