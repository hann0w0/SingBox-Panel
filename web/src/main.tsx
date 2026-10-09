import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import 'antd/dist/reset.css'
import './index.css'
import './console.css'
import Appearance from './Appearance'
import { ThemePreferenceProvider } from './themePreference'
import { markStandalone, registerServiceWorker } from './pwa'

// Makes the panel installable and keeps the shell available offline. No-op in
// dev and on insecure origins.
registerServiceWorker()
markStandalone()

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <ThemePreferenceProvider><Appearance /></ThemePreferenceProvider>
    </BrowserRouter>
  </React.StrictMode>,
)
