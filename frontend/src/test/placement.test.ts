import { readdirSync } from 'node:fs'
import { join, relative, resolve, sep } from 'node:path'

import { describe, expect, it } from 'vitest'

/**
 * 约定自检：测试文件必须放在对应的 `test/` 子目录里，不与业务文件混放。
 *
 * 为什么用测试来守约定，而不是只写进文档：
 * vitest 默认会匹配任意目录下的 `*.test.ts(x)`，放错位置的用例照样会被执行，
 * 于是"位置不对"永远不会暴露，直到有人翻目录才发现。用一条会失败的用例把规则钉住，
 * 违反时 `npm run test` 直接红，成本最低。
 */
// 用工作目录而不是 import.meta.url：jsdom 环境下 import.meta.url 不是 file 协议，
// fileURLToPath 会抛 "The URL must be of scheme file"。vitest 的 root 就是 config 所在目录（frontend/）。
const SRC_DIR = resolve(process.cwd(), 'src')

const TEST_FILE_PATTERN = /\.test\.tsx?$/
const TEST_DIR_NAME = 'test'

function collectTestFiles(dir: string, found: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const fullPath = join(dir, entry.name)
    if (entry.isDirectory()) {
      collectTestFiles(fullPath, found)
      continue
    }
    if (TEST_FILE_PATTERN.test(entry.name)) {
      found.push(fullPath)
    }
  }
  return found
}

describe('测试文件位置约定', () => {
  it('所有测试文件都位于 test/ 目录下', () => {
    const misplaced = collectTestFiles(SRC_DIR)
      .filter((filePath) => {
        const segments = relative(SRC_DIR, filePath).split(sep)
        // 去掉文件名后看路径里有没有一段叫 test（src/test/ 本身也算）。
        return !segments.slice(0, -1).includes(TEST_DIR_NAME)
      })
      .map((filePath) => relative(SRC_DIR, filePath))

    expect(misplaced).toEqual([])
  })
})
