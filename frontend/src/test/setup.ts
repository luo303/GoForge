import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// 每个用例后卸载渲染树：不清理时上一个用例的 DOM 会留在 document 里，
// 后续 getByText 可能命中"上一个页面"，造成看似随机的通过/失败。
afterEach(() => {
  cleanup()
})
