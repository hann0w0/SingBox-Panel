import { useState } from 'react'
import { Navigate, useNavigate, useSearchParams } from 'react-router-dom'
import { Plus } from 'lucide-react'
import { Button } from '../../../ui'
import { useAuth } from '../../../store'
import { PageHeader } from '../../../ui/Page'
import Users from './Users'
import { UserAccountDialog } from './UserDialogs'
import '../../../styles/users.css'

export default function UsersPage() {
  const [params] = useSearchParams()
  const user = useAuth((state) => state.user)
  const navigate = useNavigate()
  const [creating, setCreating] = useState(false)
  // Bookmarks to the former subscription tab now open the administrator's row.
  if (params.get('tab') === 'subscription' && user) return <Navigate to={`/admin/users/${user.id}`} replace />
  return <div className="um-page">
    <PageHeader
      title="用户"
      actions={<Button type="primary" icon={<Plus />} onClick={() => setCreating(true)}>新增用户</Button>}
    />
    <Users />
    {creating && <UserAccountDialog onClose={() => setCreating(false)} onSaved={(created) => navigate(`/admin/users/${created.id}`)} />}
  </div>
}
