import { lazy, Suspense } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { useAuth } from './store'

// Each screen is loaded only when its route is visited. This keeps the login
// and user pages from downloading the much larger admin forms up front.
const AppLayout = lazy(() => import('./components/Layout'))
const Login = lazy(() => import('./pages/Login'))
const Overview = lazy(() => import('./pages/admin/Overview'))
const Servers = lazy(() => import('./pages/admin/Servers'))
const ServerDetail = lazy(() => import('./pages/admin/ServerDetail'))
const Logs = lazy(() => import('./pages/admin/Logs'))
const Users = lazy(() => import('./pages/admin/Users'))
const Dashboard = lazy(() => import('./pages/user/Dashboard'))

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
  return <Navigate to={user?.role === 'admin' ? '/admin/overview' : '/dashboard'} replace />
}

export default function App() {
  return (
    <Suspense fallback={<div style={{ padding: 32, textAlign: 'center', color: '#8c8c8c' }}>加载中...</div>}>
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
          <Route path="/dashboard" element={<Dashboard />} />
          <Route path="/admin/overview" element={<AdminGuard><Overview /></AdminGuard>} />
          <Route path="/admin/servers" element={<AdminGuard><Servers /></AdminGuard>} />
          <Route path="/admin/servers/:id" element={<AdminGuard><ServerDetail /></AdminGuard>} />
          <Route path="/admin/users" element={<AdminGuard><Users /></AdminGuard>} />
          <Route path="/admin/logs" element={<AdminGuard><Logs /></AdminGuard>} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Suspense>
  )
}
