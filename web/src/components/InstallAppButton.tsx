import { Button, Tooltip } from 'antd'
import { DownloadOutlined } from '@ant-design/icons'
import { useInstallPrompt } from '../pwa'

/**
 * Renders only while the browser considers the panel installable — Chrome fires
 * `beforeinstallprompt` at that point and suspends its own install UI, so this
 * button is the only way to reach the native install sheet. On iOS (where the
 * event never fires) and once installed, this renders nothing.
 */
export default function InstallAppButton({ compact = false }: { compact?: boolean }) {
  const { canInstall, install } = useInstallPrompt()
  if (!canInstall) return null

  return (
    <Button
      type="text"
      className="console-install-app"
      icon={<DownloadOutlined />}
      title="安装到桌面"
      aria-label="安装到桌面"
      onClick={() => {
        // Dismissal is a normal outcome; the button simply stays available.
        void install().catch(() => undefined)
      }}
    >
      {compact ? '' : '安装'}
    </Button>
  )
}
