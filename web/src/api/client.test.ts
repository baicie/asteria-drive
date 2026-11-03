import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, apiPage, apiResponse } from './client'

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  })
}

/** Assert a promise rejects with an ApiError and return it with its concrete type restored. */
async function catchApiError(promise: Promise<unknown>): Promise<ApiError> {
  const error = await promise.then(
    () => { throw new Error('expected the request to reject') },
    (reason: unknown) => reason,
  )
  expect(error).toBeInstanceOf(ApiError)
  return error as ApiError
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('apiResponse', () => {
  it('attaches the bearer token and JSON content type', async () => {
    const fetchMock = vi.fn(async () => jsonResponse(200, { data: { ok: true } }))
    vi.stubGlobal('fetch', fetchMock)

    await apiResponse('/api/v1/thing', { method: 'POST', token: 'secret-token', body: JSON.stringify({ a: 1 }) })

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    const headers = init.headers as Headers
    expect(headers.get('Authorization')).toBe('Bearer secret-token')
    expect(headers.get('Content-Type')).toBe('application/json')
  })

  it('does not set Content-Type for FormData bodies', async () => {
    const fetchMock = vi.fn(async () => jsonResponse(200, { data: {} }))
    vi.stubGlobal('fetch', fetchMock)

    const form = new FormData()
    form.append('field', 'value')
    await apiResponse('/api/v1/upload', { method: 'POST', body: form })

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect((init.headers as Headers).has('Content-Type')).toBe(false)
  })

  it('preserves an explicitly provided Authorization header', async () => {
    const fetchMock = vi.fn(async () => jsonResponse(200, { data: {} }))
    vi.stubGlobal('fetch', fetchMock)

    await apiResponse('/api/v1/thing', { token: 'ignored', headers: { Authorization: 'Bearer explicit' } })

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect((init.headers as Headers).get('Authorization')).toBe('Bearer explicit')
  })

  it('throws a structured ApiError with the server error message', async () => {
    vi.stubGlobal('fetch', vi.fn(async () =>
      jsonResponse(409, { error: { code: 'name_conflict', message: '名称已存在' } })))

    const error = await catchApiError(apiResponse('/api/v1/nodes', { method: 'POST' }))

    expect(error.status).toBe(409)
    expect(error.message).toBe('名称已存在')
    expect(error.body).toEqual({ error: { code: 'name_conflict', message: '名称已存在' } })
  })

  it('falls back to a status-based message when the body is not JSON', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>502</html>', { status: 502 })))

    const error = await catchApiError(apiResponse('/api/v1/thing'))

    expect(error.status).toBe(502)
    expect(error.message).toBe('请求失败（HTTP 502）')
  })

  it('returns null for a 204 without parsing a body', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 204 })))

    await expect(apiResponse('/api/v1/uploads/1', { method: 'DELETE' })).resolves.toBeNull()
  })
})

describe('api', () => {
  it('unwraps the data envelope', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(200, { data: { id: 'node-1' } })))

    await expect(api<{ id: string }>('/api/v1/nodes/node-1')).resolves.toEqual({ id: 'node-1' })
  })

  it('passes through a body that is not enveloped', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(200, { id: 'node-1' })))

    await expect(api<{ id: string }>('/api/v1/nodes/node-1')).resolves.toEqual({ id: 'node-1' })
  })

  it('returns null for an enveloped null', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(200, { data: null })))

    await expect(api('/api/v1/nodes/node-1')).resolves.toBeNull()
  })
})

describe('apiPage', () => {
  it('returns a valid page', async () => {
    vi.stubGlobal('fetch', vi.fn(async () =>
      jsonResponse(200, { data: [{ id: 'a' }], page: { next_cursor: 'cursor-2' } })))

    const page = await apiPage<{ id: string }>('/api/v1/files?limit=100')
    expect(page.data).toHaveLength(1)
    expect(page.page.next_cursor).toBe('cursor-2')
  })

  it.each([
    ['missing data', { page: { next_cursor: null } }],
    ['non-array data', { data: {}, page: { next_cursor: null } }],
    ['missing page', { data: [] }],
    ['numeric cursor', { data: [], page: { next_cursor: 7 } }],
    ['null body', null],
  ])('rejects an invalid page envelope: %s', async (_label, body) => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(200, body)))

    await expect(apiPage('/api/v1/files')).rejects.toThrow('分页响应格式无效')
  })
})
