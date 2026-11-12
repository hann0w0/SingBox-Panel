import { lazy, Suspense } from 'react'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { useAuth } from './store'
import { DialogHost } from './ui/dialogs'
import { ToastHost } from './ui/feedback'
import { RouteErrorBoundary } from './components/RouteErrorBoundary'

// Each screen is loaded only when its route is visited. This keeps the login
// and user pages from downloading the much larger admin forms up front.
const AppLayout = lazy(() => import('./components/Layout'))
const Login = lazy(() => import('./features/auth/Login'))
const Servers = lazy(() => import('./features/admin/servers/Servers'))
const ServerDetail = lazy(() => import('./features/admin/servers/ServerDetail'))
const ExternalNodes = lazy(() => import('./features/admin/nodes/ExternalNodes'))
const Traffic = lazy(() => import('./features/admin/traffic/Traffic'))
const Logs = lazy(() => import('./features/admin/logs/Logs'))
const Users = lazy(() => import('./features/admin/users/UsersPage'))
const UserDetail = lazy(() => import('./features/admin/users/UserDetail'))
const Settings = lazy(() => import('./features/admin/settings/Settings'))
const Dashboard = lazy(() => import('./features/user/Dashboard'))

function Protected({ children }: { children: JSX.Element }) {
  const token = useAuth((s) => s.token)
  if (!token) return <Navigate to="/login" replace />
  return children
}

function AdminGuard({ children }: { children: JSX.Element }) {
  const user = useAuth((s) => s.user)
  if (user?.role !== 'admin') return <Navigate to="/dashboard" replace />
  return children
}

function Home() {
  const user = useAuth((s) => s.user)
  return <Navigate to={user?.role === 'admin' ? '/admin/servers' : '/dashboard'} replace />
}

function DashboardRoute() {
  const user = useAuth((s) => s.user)
  return user?.role === 'admin' ? <Navigate to={`/admin/users/${user.id}`} replace /> : <Dashboard />
}

// Spinner renders an Apple-style 12-spoke throbber (pure CSS, no icon font).
function Spinner({ size }: { size?: 'lg' }) {
  return (
    <div className={`app-spinner${size === 'lg' ? ' lg' : ''}`} role="status" aria-label="加载中">
      {Array.from({ length: 12 }).map((_, i) => (
        <span
          key={i}
          style={{
            transform: `rotate(${i * 30}deg)`,
            animationDelay: `${(i - 12) / 12}s`,
          }}
        />
      ))}
    </div>
  )
}

function PageFallback() {
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', minHeight: '50vh', color: 'var(--console-faint)' }}>
      <Spinner size="lg" />
    </div>
  )
}

export default function App() {
  const location = useLocation()
  return (
    <>
      <DialogHost />
      <ToastHost />
      <RouteErrorBoundary resetKey={location.pathname}>
        <Suspense fallback={<PageFallback />}>
        <Routes>
        <Route path="/login" element={<Login />} />
        <Route
          element={
            <Protected>
              <AppLayout />
            </Protected>
          }
        >
          <Route path="/" element={<Home />} />
          <Route path="/dashboard" element={<DashboardRoute />} />
          <Route path="/admin/overview" element={<Navigate to="/admin/servers" replace />} />
          <Route path="/admin/servers" element={<AdminGuard><Servers /></AdminGuard>} />
          <Route path="/admin/servers/:id/:section?" element={<AdminGuard><ServerDetail /></AdminGuard>} />
          <Route path="/admin/traffic" element={<AdminGuard><Traffic /></AdminGuard>} />
          <Route path="/admin/users" element={<AdminGuard><Users /></AdminGuard>} />
          <Route path="/admin/users/:id" element={<AdminGuard><UserDetail /></AdminGuard>} />
          <Route path="/admin/logs" element={<AdminGuard><Logs /></AdminGuard>} />
          <Route path="/admin/external-nodes" element={<AdminGuard><ExternalNodes /></AdminGuard>} />
          <Route path="/admin/settings" element={<AdminGuard><Settings /></AdminGuard>} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
        </Suspense>
      </RouteErrorBoundary>
    </>
  )
}
