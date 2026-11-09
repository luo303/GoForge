import { apiRequest } from '@/api/client'
import type { SettingsView, SystemStatus } from '@/api/types'

/**
 * 查询键集中登记。
 *
 * TanStack Query 靠"键"判断缓存归属，键散落在各组件里时，
 * 一处写 ['system','status'] 另一处写 ['system-status'] 就会各存一份、互不刷新。
 */
export const queryKeys = {
  systemStatus: ['system', 'status'] as const,
  settings: ['settings'] as const,
}

export function fetchSystemStatus(signal?: AbortSignal): Promise<SystemStatus> {
  return apiRequest<SystemStatus>('/system/status', { signal })
}

export function fetchSettings(signal?: AbortSignal): Promise<SettingsView> {
  return apiRequest<SettingsView>('/settings', { signal })
}
