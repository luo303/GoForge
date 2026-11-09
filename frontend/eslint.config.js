import js from '@eslint/js'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import globals from 'globals'
import tseslint from 'typescript-eslint'

// ESLint 9+ 只用扁平配置（flat config），不再读 .eslintrc。
// 规则分三块：JS 推荐集、TS 推荐集（不含需要类型信息的规则，跑得更快）、React 两个插件。
export default tseslint.config(
  { ignores: ['dist', 'node_modules', 'coverage'] },
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      ...tseslint.configs.recommended,
      // react-hooks v7 同时发布两种形态：configs.flat.* 是扁平配置，
      // configs['recommended-latest'] 仍是旧式 eslintrc 结构（plugins 为字符串数组），
      // 在 ESLint 9+ 会直接报错，所以必须取 flat 这一支。
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      ecmaVersion: 2022,
      globals: globals.browser,
    },
  },
)
