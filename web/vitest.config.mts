import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    projects: [
      {
        extends: './frpc/vite.config.mts',
        root: './frpc',
        test: {
          name: 'frpc',
          environment: 'jsdom',
          css: true,
          server: {
            deps: { inline: ['element-plus', '@element-plus/icons-vue'] },
          },
          setupFiles: [],
          include: [
            'test/**/*.test.ts',
            '../shared/**/*.test.ts',
          ],
        },
      },
    ],
  },
})
