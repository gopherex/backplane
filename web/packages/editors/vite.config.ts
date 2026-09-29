import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
export default defineConfig({ plugins: [react()], build: { lib: { entry: 'src/index.tsx', formats: ['es'], fileName: 'index' }, rollupOptions: { external: (id) => !id.startsWith('.') && !id.startsWith('/') } } });
