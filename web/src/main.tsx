import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import 'antd/dist/reset.css'
import './index.css'
import './console.css'
import Appearance from './Appearance'
import { ThemePreferenceProvider } from './themePreference'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <ThemePreferenceProvider><Appearance /></ThemePreferenceProvider>
    </BrowserRouter>
  </React.StrictMode>,
)
