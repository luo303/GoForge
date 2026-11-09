import type { HTMLAttributes } from 'react'

import { cn } from '@/lib/utils'

export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      data-slot="card"
      className={cn(
        'bg-card text-card-foreground flex flex-col gap-4 rounded-xl border py-5 shadow-sm',
        className,
      )}
      {...props}
    />
  )
}

export function CardHeader({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div data-slot="card-header" className={cn('flex flex-col gap-1 px-5', className)} {...props} />
}

export function CardTitle({ className, ...props }: HTMLAttributes<HTMLHeadingElement>) {
  return <h3 data-slot="card-title" className={cn('font-semibold leading-none', className)} {...props} />
}

export function CardDescription({ className, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return (
    <p data-slot="card-description" className={cn('text-muted-foreground text-sm', className)} {...props} />
  )
}

export function CardContent({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div data-slot="card-content" className={cn('px-5', className)} {...props} />
}

export function CardFooter({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div data-slot="card-footer" className={cn('flex items-center px-5', className)} {...props} />
}
