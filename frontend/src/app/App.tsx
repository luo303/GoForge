import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

export default function App() {
  return (
    <main className="flex min-h-full items-center justify-center p-8">
      <Card className="w-full max-w-md">
        <CardHeader>
          <CardTitle>MindOS 前端工程已就绪</CardTitle>
          <CardDescription>Vite + React + TypeScript + Tailwind + shadcn/ui</CardDescription>
        </CardHeader>
        <CardContent className="text-muted-foreground text-sm">
          应用布局与路由在下一次提交接入。
        </CardContent>
      </Card>
    </main>
  )
}
