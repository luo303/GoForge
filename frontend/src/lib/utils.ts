import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/**
 * 合并 className：clsx 处理条件拼接，twMerge 解决 Tailwind 冲突。
 *
 * 为什么必须有 twMerge：`<Card className="p-6">` 这类覆盖很常见，
 * 但 `p-4 p-6` 在 CSS 里的胜者取决于生成顺序而不是书写顺序。
 * twMerge 按"后者胜"把冲突类去掉，覆盖才符合直觉。
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
