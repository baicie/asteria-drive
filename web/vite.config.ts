import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [
    vue({
      template: {
        compilerOptions: {
          isCustomElement: (tag) => tag.startsWith('zw-'),
        },
      },
    }),
  ],
  server: {
    host: '0.0.0.0',
    port: 5173,
  },
  resolve: {
    dedupe: ['vue'],
  },
})
