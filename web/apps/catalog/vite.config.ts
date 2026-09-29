import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { grafanaAssets } from '@gopherex/backplane-plugin-build';

export default defineConfig({ plugins: [grafanaAssets(), react()], base: './', resolve: { dedupe: ['react', 'react-dom'] } });
