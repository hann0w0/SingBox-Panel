import { create } from 'zustand'
import { persist, createJSONStorage, type StateStorage } from 'zustand/middleware'
import type { User } from './types'

interface AuthState {
  token: string | null
  user: User | null
  setAuth: (token: string, user: User) => void
  setUser: (user: User) => void
  logout: () => void
}

const OLD_TOKEN_KEY = 'singbox-panel_token'
const OLD_USER_KEY = 'singbox-panel_user'

// 内存存储兜底，适配 SSR 或无 localStorage 的测试环境
const memoryStorage: StateStorage = {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
}

const safeStorage: StateStorage = {
  getItem: (name: string) => {
    try {
      if (typeof window !== 'undefined' && window.localStorage) {
        const item = window.localStorage.getItem(name)
        if (item) return item
        // 兼容旧版 localStorage 存储的 key 进行自动平滑迁移
        const oldToken = window.localStorage.getItem(OLD_TOKEN_KEY)
        if (oldToken) {
          let oldUser: User | null = null
          try {
            const rawUser = window.localStorage.getItem(OLD_USER_KEY)
            if (rawUser) oldUser = JSON.parse(rawUser)
          } catch {
            // ignore JSON parse error
          }
          const migratedState = JSON.stringify({
            state: { token: oldToken, user: oldUser },
            version: 0,
          })
          window.localStorage.removeItem(OLD_TOKEN_KEY)
          window.localStorage.removeItem(OLD_USER_KEY)
          window.localStorage.setItem(name, migratedState)
          return migratedState
        }
      }
    } catch {
      // ignore security / quota errors
    }
    return memoryStorage.getItem(name)
  },
  setItem: (name: string, value: string) => {
    try {
      if (typeof window !== 'undefined' && window.localStorage) {
        window.localStorage.setItem(name, value)
        return
      }
    } catch {
      // ignore
    }
    memoryStorage.setItem(name, value)
  },
  removeItem: (name: string) => {
    try {
      if (typeof window !== 'undefined' && window.localStorage) {
        window.localStorage.removeItem(name)
        window.localStorage.removeItem(OLD_TOKEN_KEY)
        window.localStorage.removeItem(OLD_USER_KEY)
        return
      }
    } catch {
      // ignore
    }
    memoryStorage.removeItem(name)
  },
}

export const useAuth = create<AuthState>()(
  persist(
    (set) => ({
      token: null,
      user: null,
      setAuth: (token, user) => set({ token, user }),
      setUser: (user) => set({ user }),
      logout: () => set({ token: null, user: null }),
    }),
    {
      name: 'singbox-panel-auth',
      storage: createJSONStorage(() => safeStorage),
    },
  ),
)
