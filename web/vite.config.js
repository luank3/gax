import { defineConfig } from 'vite';
import preact from '@preact/preset-vite';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  plugins: [preact(), tailwindcss()],
  build: {
    outDir: '../internal/serve/ui/dist',
    emptyOutDir: true,
  },
});
