import { App as AntApp, message as staticMessage, Modal as staticModal, notification as staticNotification } from 'antd'
import type { MessageInstance } from 'antd/es/message/interface'
import type { ModalStaticFunctions } from 'antd/es/modal/confirm'
import type { NotificationInstance } from 'antd/es/notification/interface'

let messageInstance: MessageInstance = staticMessage
let notificationInstance: NotificationInstance = staticNotification
let modalInstance: Omit<ModalStaticFunctions, 'warn'> = staticModal

export const setAntdApp = (app: {
  message: MessageInstance
  notification: NotificationInstance
  modal: Omit<ModalStaticFunctions, 'warn'>
}) => {
  messageInstance = app.message
  notificationInstance = app.notification
  modalInstance = app.modal
}

// 动态代理，确保在 React 组件外部调用时优先使用注入了 Context 的实例，
// 在未挂载时自动 fallback 到 static 实例，避免任何 undefined 报错。
export const message: MessageInstance = new Proxy({} as MessageInstance, {
  get(_target, prop: string | symbol) {
    const fn = (messageInstance as unknown as Record<string | symbol, unknown>)[prop]
    if (typeof fn === 'function') {
      return fn.bind(messageInstance)
    }
    return fn
  },
})

export const modal: Omit<ModalStaticFunctions, 'warn'> = new Proxy({} as Omit<ModalStaticFunctions, 'warn'>, {
  get(_target, prop: string | symbol) {
    const fn = (modalInstance as unknown as Record<string | symbol, unknown>)[prop]
    if (typeof fn === 'function') {
      return fn.bind(modalInstance)
    }
    return fn
  },
})

export const notification: NotificationInstance = new Proxy({} as NotificationInstance, {
  get(_target, prop: string | symbol) {
    const fn = (notificationInstance as unknown as Record<string | symbol, unknown>)[prop]
    if (typeof fn === 'function') {
      return fn.bind(notificationInstance)
    }
    return fn
  },
})

/**
 * 放在根组件内部，自动将 Antd App 上下文桥接到助手模块中
 */
export function AntdAppHolder() {
  const staticApp = AntApp.useApp()
  setAntdApp(staticApp)
  return null
}
