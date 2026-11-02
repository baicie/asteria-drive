import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { RecycleEntry } from '../../api/types'
import { useRecycleBin } from './use-recycle-bin'

function entry(id: string, name = id): RecycleEntry {
  const node = { id, parent_id: 'root-1', kind: 'file' as const, name, size: 1, revision: 1 }
  return { node, original_parent_id: 'root-1', deleted_at: '2026-10-01T00:00:00Z' }
}

function page(data: unknown[], cursor: string | null = null) {
  return { data, page: { next_cursor: cursor } }
}

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

function makeBin() {
  const notices: Array<{ text: string; kind?: string }> = []
  const bin = useRecycleBin({
    token: ref('test-token'),
    notify: (text, kind) => notices.push({ text, kind }),
    clearNotice: () => notices.push({ text: '', kind: 'clear' }),
  })
  return { bin, notices }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useRecycleBin load', () => {
  it('reads the first page with the bearer token', async () => {
    const fetchMock = vi.fn(async () => json(page([entry('n1')], 'cursor-2')))
    vi.stubGlobal('fetch', fetchMock)
    const { bin } = makeBin()

    await bin.load()

    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect((init.headers as Headers).get('Authorization')).toBe('Bearer test-token')
    expect(String((fetchMock.mock.calls[0] as unknown as [string])[0])).toBe('/api/v1/recycle-bin?limit=100')
    expect(bin.entries.value.map((item) => item.node.id)).toEqual(['n1'])
    expect(bin.cursor.value).toBe('cursor-2')
    expect(bin.loading.value).toBe(false)
  })

  it('drops the previous entries on refresh instead of appending', async () => {
    const responses = [json(page([entry('old1'), entry('old2')])), json(page([entry('new1')]))]
    vi.stubGlobal('fetch', vi.fn(async () => responses.shift() ?? json(page([]))))
    const { bin } = makeBin()

    await bin.load()
    await bin.load()

    expect(bin.entries.value.map((item) => item.node.id)).toEqual(['new1'])
  })

  it('reports a failure and clears the loading flag', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new TypeError('offline')
    }))
    const { bin, notices } = makeBin()

    await bin.load()

    expect(bin.loading.value).toBe(false)
    expect(notices.filter((notice) => notice.kind === 'error')).toHaveLength(1)
  })

  it('ignores a response from a superseded refresh', async () => {
    const pending: Array<(value: Response) => void> = []
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => pending.push(resolve))))
    const { bin } = makeBin()

    const first = bin.load()
    const second = bin.load()
    // The newer request lands first; the older one must not overwrite its result.
    pending[1](json(page([entry('new')])))
    await second
    pending[0](json(page([entry('stale')])))
    await first

    expect(bin.entries.value.map((item) => item.node.id)).toEqual(['new'])
    expect(bin.loading.value).toBe(false)
  })

  it('does not report an error for a superseded failing refresh', async () => {
    const pending: Array<{ resolve: (value: Response) => void; reject: (reason: unknown) => void }> = []
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve, reject) => pending.push({ resolve, reject }))))
    const { bin, notices } = makeBin()

    const first = bin.load()
    const second = bin.load()
    pending[1].resolve(json(page([])))
    await second
    pending[0].reject(new TypeError('offline'))
    await first

    expect(notices.filter((notice) => notice.kind === 'error')).toHaveLength(0)
  })
})

describe('useRecycleBin loadMore', () => {
  it('appends the next page and deduplicates repeated ids', async () => {
    const responses = [
      json(page([entry('n1')], 'cursor-2')),
      json(page([entry('n1'), entry('n2')], null)),
    ]
    vi.stubGlobal('fetch', vi.fn(async () => responses.shift() ?? json(page([]))))
    const { bin } = makeBin()

    await bin.load()
    await bin.loadMore()

    expect(bin.entries.value.map((item) => item.node.id)).toEqual(['n1', 'n2'])
    expect(bin.cursor.value).toBeNull()
  })

  it('encodes the cursor and refuses when there is none', async () => {
    const fetchMock = vi.fn(async () => json(page([entry('n1')], 'a/b+c')))
    vi.stubGlobal('fetch', fetchMock)
    const { bin } = makeBin()

    await bin.loadMore()
    expect(fetchMock).not.toHaveBeenCalled()

    await bin.load()
    fetchMock.mockClear()
    await bin.loadMore()
    expect(String((fetchMock.mock.calls[0] as unknown as [string])[0])).toBe('/api/v1/recycle-bin?limit=100&cursor=a%2Fb%2Bc')
  })

  it('surfaces a repeated cursor instead of looping forever', async () => {
    const responses = [
      json(page([entry('n1')], 'cursor-2')),
      json(page([entry('n2')], 'cursor-2')),
    ]
    vi.stubGlobal('fetch', vi.fn(async () => responses.shift() ?? json(page([]))))
    const { bin, notices } = makeBin()

    await bin.load()
    await bin.loadMore()

    expect(notices.some((notice) => notice.text.includes('重复的分页游标'))).toBe(true)
    expect(bin.loadingMore.value).toBe(false)
  })
})

describe('useRecycleBin reset', () => {
  it('clears everything and abandons an in-flight request', async () => {
    const pending: Array<(value: Response) => void> = []
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => pending.push(resolve))))
    const { bin, notices } = makeBin()

    const loading = bin.load()
    bin.reset()
    pending[0](json(page([entry('late')])))
    await loading

    expect(bin.entries.value).toEqual([])
    expect(bin.cursor.value).toBeNull()
    expect(bin.loading.value).toBe(false)
    expect(notices.filter((notice) => notice.kind === 'error')).toHaveLength(0)
  })
})
