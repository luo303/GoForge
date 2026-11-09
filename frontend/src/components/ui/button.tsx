import { cva, type VariantProps } from 'class-variance-authority'
import type { ButtonHTMLAttributes } from 'react'

import { cn } from '@/lib/utils'

/**
 * 按钮样式表（shadcn/ui 结构）：变体用 cva 声明，默认值集中在 defaultVariants。
 *
 * 没有实现 shadcn 的 `asChild`：那需要引入 @radix-ui/react-slot，
 * 而项目里"按钮样式的链接"用 `buttonVariants()` 直接给 Link 加类就够了，
 * 不值得为语法糖多一个运行时依赖。
 */
// 只在文件内使用，不导出：组件目录的 .tsx 若同时导出函数与组件，
// react-refresh 无法判断该热更新组件还是模块，ESLint 会直接报错。
const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-all disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg:not([class*='size-'])]:size-4 shrink-0 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50",
  {
    variants: {
      variant: {
        default: 'bg-primary text-primary-foreground shadow-xs hover:bg-primary/90',
        destructive: 'bg-destructive text-white shadow-xs hover:bg-destructive/90',
        outline: 'border bg-background shadow-xs hover:bg-accent hover:text-accent-foreground',
        secondary: 'bg-secondary text-secondary-foreground shadow-xs hover:bg-secondary/80',
        ghost: 'hover:bg-accent hover:text-accent-foreground',
        link: 'text-primary underline-offset-4 hover:underline',
      },
      size: {
        default: 'h-9 px-4 py-2 has-[>svg]:px-3',
        sm: 'h-8 gap-1.5 rounded-md px-3 has-[>svg]:px-2.5',
        lg: 'h-10 rounded-md px-6 has-[>svg]:px-4',
        icon: 'size-9',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  },
)

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof buttonVariants>

export function Button({ className, variant, size, type = 'button', ...props }: ButtonProps) {
  // type 默认 button：不写的话 <button> 落在 form 里默认 submit，
  // 这是个只有触发时才发现的坑，所以组件层统一兜底。
  return (
    <button
      data-slot="button"
      type={type}
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  )
}
