import type { PageResponse } from './types'

export class ApiError extends Error {
  readonly status: number
  readonly body: unknown

  constructor(message: string, status: number, body: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
}

function errorMessage(body: unknown, status: number) {
  if (body && typeof body === 'object') {
    const value = body as { error?: { message?: string }; message?: string }
    if (value.error?.message) return value.error.message
    if (value.message) return value.message
  }
  return `请求失败（HTTP ${status}）`
}

export interface ApiInit extends RequestInit {
  token?: string
}

export async function apiResponse<T>(path: string, init: ApiInit = {}): Promise<T> {
  const { token, ...requestInit } = init
  const headers = new Headers(requestInit.headers)
  if (token && !headers.has('Authorization')) headers.set('Authorization', `Bearer ${token}`)
  if (requestInit.body && !(requestInit.body instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  const response = await fetch(path, { ...requestInit, headers })
  const body: unknown = response.status === 204 ? null : await response.json().catch(() => null)
  if (!response.ok) throw new ApiError(errorMessage(body, response.status), response.status, body)
  return body as T
}

export async function api<T>(path: string, init: ApiInit = {}): Promise<T> {
  const body = await apiResponse<{ data?: T } | T>(path, init)
  return ((body as { data?: T } | null)?.data ?? body) as T
}

export async function apiPage<T>(path: string, token = ''): Promise<PageResponse<T>> {
  const response = await apiResponse<PageResponse<T>>(path, { token })
  if (!response || !Array.isArray(response.data) || !response.page ||
      (response.page.next_cursor !== null && typeof response.page.next_cursor !== 'string')) {
    throw new Error('分页响应格式无效')
  }
  return response
}
