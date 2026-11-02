import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useDirectory } from './use-directory'

const rootId = 'root-1'
const otherId = 'other-2'

function makeDirectory() {
  const notices: Array<{ text: string; kind?: string }> = []
  const directory = useDirectory({
    token: ref('test-token'),
    notify: (text, kind) => notices.push({ text, kind }),
    clearNotice: () => notices.push({ text: '', kind: 'clear' }),
  })
  return { directory, notices }
}

function page(items: Array<{ id: string; kind: string; name: string }>, cursor: string | null = null) {
  return { data: items, page: { next_cursor: cursor } }
}

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

/** A fetch stub whose responses are resolved manually, to control interleaving. */
function deferredFetch() {
  const pending: Array<{ url: string; resolve: (value: Response) => void; reject: (reason: unknown) => void }> = []
  const fetchMock = vi.fn((url: string) => new Promise<Response>((resolve, reject) => {
    pending.push({ url: String(url), resolve, reject })
  }))
  vi.stubGlobal('fetch', fetchMock)
  return {
    pending,
    fetchMock,
    async settle(index: number, body: unknown) {
      pending[index].resolve(json(body))
      await Promise.resolve()
      await Promise.resolve()
    },
    async fail(index: number, reason: unknown) {
      pending[index].reject(reason)
      await Promise.resolve()
      await Promise.resolve()
    },
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useDirectory load', () => {
  it('loads a directory and its children together', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) =>
      String(url).endsWith('/children?limit=100')
        ? json(page([{ id: 'f1', kind: 'file', name: 'a.txt' }]))
        : json({ data: { id: rootId, kind: 'directory', name: '根目录', revision: 1 } })))

    const { directory } = makeDirectory()
    const loaded = await directory.load(rootId, [{ id: rootId, name: '根目录' }])

    expect(loaded).toBe(true)
    expect(directory.entries.value).toHaveLength(1)
    expect(directory.fileCount.value).toBe(1)
    expect(directory.loading.value).toBe(false)
  })

  it('discards a stale response when a newer navigation started', async () => {
    const net = deferredFetch()
    const { directory } = makeDirectory()

    const first = directory.load(rootId, [{ id: rootId, name: '根目录' }])
    const second = directory.load(otherId, [{ id: otherId, name: '其他' }])

    // Resolve the newer request first, then let the older one land.
    await net.settle(2, { data: { id: otherId, kind: 'directory', name: '其他', revision: 1 } })
    await net.settle(3, page([{ id: 'new', kind: 'file', name: 'new.txt' }]))
    await net.settle(0, { data: { id: rootId, kind: 'directory', name: '根目录', revision: 1 } })
    await net.settle(1, page([{ id: 'stale', kind: 'file', name: 'stale.txt' }]))

    await expect(first).resolves.toBe(false)
    await expect(second).resolves.toBe(true)
    expect(directory.currentDirectory.value?.id).toBe(otherId)
    expect(directory.entries.value.map((node) => node.id)).toEqual(['new'])
  })

  it('clears loading even when the request fails', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new TypeError('offline')
    }))
    const { directory, notices } = makeDirectory()

    await expect(directory.load(rootId, [])).resolves.toBe(false)

    expect(directory.loading.value).toBe(false)
    expect(notices.some((notice) => notice.kind === 'error')).toBe(true)
  })

  it('does not report an error for a superseded failed request', async () => {
    const net = deferredFetch()
    const { directory, notices } = makeDirectory()

    void directory.load(rootId, [])
    const second = directory.load(otherId, [])

    await net.fail(0, new TypeError('offline'))
    await net.settle(2, { data: { id: otherId, kind: 'directory', name: '其他', revision: 1 } })
    await net.settle(3, page([]))

    await expect(second).resolves.toBe(true)
    expect(notices.filter((notice) => notice.kind === 'error')).toHaveLength(0)
  })
})

describe('useDirectory loadMore', () => {
  it('appends a page and deduplicates repeated ids', async () => {
    const responses = [
      json(page([{ id: 'a', kind: 'file', name: 'a' }], 'cursor-2')),
      json(page([{ id: 'a', kind: 'file', name: 'a' }, { id: 'b', kind: 'file', name: 'b' }], null)),
    ]
    vi.stubGlobal('fetch', vi.fn(async () => responses.shift() ?? json(page([]))))
    const { directory } = makeDirectory()

    await directory.load(rootId, [])
    await directory.loadMore()

    expect(directory.entries.value.map((node) => node.id)).toEqual(['a', 'b'])
    expect(directory.cursor.value).toBeNull()
  })

  it('ignores a repeated next_cursor and surfaces an error', async () => {
    const childrenPages = [
      json(page([{ id: 'a', kind: 'file', name: 'a' }], 'cursor-2')),
      json(page([{ id: 'b', kind: 'file', name: 'b' }], 'cursor-2')),
    ]
    vi.stubGlobal('fetch', vi.fn(async (url: string) =>
      String(url).includes('/children') ? childrenPages.shift() ?? json(page([]))
        : json({ data: { id: rootId, kind: 'directory', name: '根目录', revision: 1 } })))
    const { directory, notices } = makeDirectory()

    await directory.load(rootId, [])
    await directory.loadMore()

    expect(notices.some((notice) => notice.text.includes('重复的分页游标'))).toBe(true)
  })

  it('does nothing without a cursor or while a load is in flight', async () => {
    const fetchMock = vi.fn(async () => json(page([{ id: 'a', kind: 'file', name: 'a' }], null)))
    vi.stubGlobal('fetch', fetchMock)
    const { directory } = makeDirectory()

    await directory.load(rootId, [])
    fetchMock.mockClear()
    await directory.loadMore()

    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('drops a loadMore page that returns after navigating elsewhere', async () => {
    const children = [
      page([{ id: 'a', kind: 'file', name: 'a' }], 'cursor-2'),
      page([{ id: 'stale', kind: 'file', name: 'stale' }], 'cursor-3'),
      page([{ id: 'new', kind: 'file', name: 'new' }], null),
    ]
    const pending: Array<(value: Response) => void> = []
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (!String(url).includes('/children')) {
        const id = String(url).includes(otherId) ? otherId : rootId
        const name = id === otherId ? '其他' : '根目录'
        return Promise.resolve(json({ data: { id, kind: 'directory', name, revision: 1 } }))
      }
      // Children responses resolve only when the test says so.
      return new Promise<Response>((resolve) => pending.push(resolve))
    }))
    const { directory } = makeDirectory()

    const initial = directory.load(rootId, [])
    pending.shift()!(json(children[0]))
    await initial

    const more = directory.loadMore()
    const navigate = directory.load(otherId, [])
    // loadMore's fetch is issued first because its prologue is synchronous.
    const moreChildren = pending.shift()!
    const navigationChildren = pending.shift()!
    navigationChildren(json(children[2])) // navigation wins
    moreChildren(json(children[1])) // stale loadMore page arrives afterwards
    await more
    await navigate

    expect(directory.currentDirectory.value?.id).toBe(otherId)
    expect(directory.entries.value.map((node) => node.id)).toEqual(['new'])
  })

  it('resets all state on logout', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) =>
      String(url).endsWith('/children?limit=100')
        ? json(page([{ id: 'a', kind: 'file', name: 'a' }], 'cursor-2'))
        : json({ data: { id: rootId, kind: 'directory', name: '根目录', revision: 1 } })))
    const { directory } = makeDirectory()

    await directory.load(rootId, [{ id: rootId, name: '根目录' }])
    directory.reset()

    expect(directory.currentDirectory.value).toBeNull()
    expect(directory.entries.value).toEqual([])
    expect(directory.breadcrumbs.value).toEqual([])
    expect(directory.cursor.value).toBeNull()
  })
})
