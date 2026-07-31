import { Navigate, Route, Routes } from 'react-router-dom'
import { useAuth } from './store'
import AppLayout from './components/Layout'
import Login from './pages/Login'
import Overview from './pages/admin/Overview'
import Servers from './pages/admin/Servers'
import ServerDetail from './pages/admin/ServerDetail'
import Logs from './pages/admin/Logs'
import Users from './pages/admin/Users'
import Dashboard from './pages/user/Dashboard'

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
  )
}
