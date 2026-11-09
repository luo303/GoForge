import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import App from '@/app/App'
import '@/index.css'

const rootElement = document.getElementById('root')

// 不用 `rootElement!` 断言：index.html 被改动时 here 会直接抛错，
// 比 createRoot(null) 的隐晦报错更容易定位。
if (!rootElement) {
  throw new Error('missing #root element in index.html')
}

createRoot(rootElement).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
