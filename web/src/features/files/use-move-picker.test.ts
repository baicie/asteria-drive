import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { Node } from '../../api/types'
import { useMovePicker } from './use-move-picker'

const rootId = 'root-1'
const folderId = 'folder-1'

function node(id: string, kind: Node['kind'], name = id): Node {
  return { id, parent_id: rootId, kind, name, revision: 1 }
}

function page(data: Node[], cursor: string | null = null) {
  return { data, page: { next_cursor: cursor } }
}

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

function makePicker() {
  return useMovePicker({ token: ref('test-token') })
}

const file = node('file-1', 'file', '报告.txt')

/** Route children requests by directory so response order cannot be confused. */
function routedFetch(responses: Record<string, Array<unknown>>) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    const match = /\/directories\/([^/]+)\/children/.exec(url)
    const key = match ? match[1] : 'other'
    const queue = responses[key]
    if (!queue || queue.length === 0) return json(page([]))
    return json(queue.shift())
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useMovePicker open', () => {
  it('shows only directories and starts at the root path', async () => {
    routedFetch({ [rootId]: [page([node(folderId, 'directory', '归档'), node('file-2', 'file', 'x.txt')])] })
    const picker = makePicker()

    await picker.open(file, rootId)

    expect(picker.target.value?.id).toBe('file-1')
    expect(picker.path.value).toEqual([{ id: rootId, name: '根目录' }])
    expect(picker.directories.value.map((item) => item.id)).toEqual([folderId])
    expect(picker.loading.value).toBe(false)
  })

  it('sends the bearer token and the large page size', async () => {
    const fetchMock = routedFetch({ [rootId]: [page([])] })
    const picker = makePicker()

    await picker.open(file, rootId)

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe(`/api/v1/directories/${rootId}/children?limit=200`)
    expect((init.headers as Headers).get('Authorization')).toBe('Bearer test-token')
  })

  it('reports a failed read and leaves no stale directories', async () => {
    routedFetch({ [rootId]: [page([node(folderId, 'directory')])] })
    const picker = makePicker()
    await picker.open(file, rootId)

    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new TypeError('offline')
    }))
    await picker.enter(picker.directories.value[0])

    expect(picker.error.value).toBeTruthy()
    expect(picker.directories.value).toEqual([])
    expect(picker.loading.value).toBe(false)
  })
})

describe('useMovePicker navigation', () => {
  it('descends into a directory and pushes it onto the path', async () => {
    const child = node('child-1', 'directory', '子目录')
    routedFetch({ [rootId]: [page([node(folderId, 'directory')])], [folderId]: [page([child])] })
    const picker = makePicker()

    await picker.open(file, rootId)
    await picker.enter(picker.directories.value[0])

    expect(picker.path.value.map((crumb) => crumb.id)).toEqual([rootId, folderId])
    expect(picker.directories.value.map((item) => item.id)).toEqual(['child-1'])
  })

  it('refuses to enter the item being moved and keeps the current directory', async () => {
    const folder = node(folderId, 'directory')
    const fetchMock = routedFetch({ [rootId]: [page([folder])] })
    const picker = makePicker()

    await picker.open(folder, rootId)
    fetchMock.mockClear()
    await picker.enter(folder)

    expect(picker.error.value).toBe('不能移动到自身')
    expect(picker.path.value.map((crumb) => crumb.id)).toEqual([rootId])
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('jump truncates the path back to the clicked crumb', async () => {
    routedFetch({
      [rootId]: [page([node(folderId, 'directory')])],
      [folderId]: [page([node('child-1', 'directory')])],
    })
    const picker = makePicker()

    await picker.open(file, rootId)
    await picker.enter(picker.directories.value[0])
    expect(picker.path.value).toHaveLength(2)

    await picker.jump(0)

    expect(picker.path.value.map((crumb) => crumb.id)).toEqual([rootId])
  })

  it('drops a response that arrives after the picker closed', async () => {
    const pending: Array<(value: Response) => void> = []
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => pending.push(resolve))))
    const picker = makePicker()

    const opening = picker.open(file, rootId)
    picker.close()
    pending[0](json(page([node(folderId, 'directory')])))
    await opening

    expect(picker.target.value).toBeNull()
    expect(picker.directories.value).toEqual([])
    expect(picker.loading.value).toBe(false)
  })

  it('drops a response from a directory the user already left', async () => {
    const pending: Array<(value: Response) => void> = []
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => pending.push(resolve))))
    const picker = makePicker()

    const opening = picker.open(file, rootId)
    const entering = picker.enter(node(folderId, 'directory'))
    // The newer request resolves first; the root listing then lands stale.
    pending[1](json(page([node('child-1', 'directory')])))
    await entering
    pending[0](json(page([node('late', 'directory', '迟到')])))
    await opening

    expect(picker.path.value.map((crumb) => crumb.id)).toEqual([rootId, folderId])
    expect(picker.directories.value.map((item) => item.id)).toEqual(['child-1'])
  })
})

describe('useMovePicker loadMore', () => {
  it('appends the next page of directories only', async () => {
    routedFetch({
      [rootId]: [
        page([node('d1', 'directory')], 'cursor-2'),
        page([node('d1', 'directory'), node('d2', 'directory'), node('f3', 'file')], null),
      ],
    })
    const picker = makePicker()

    await picker.open(file, rootId)
    await picker.loadMore()

    expect(picker.directories.value.map((item) => item.id)).toEqual(['d1', 'd2'])
    expect(picker.cursor.value).toBeNull()
  })

  it('does nothing without a cursor', async () => {
    const fetchMock = routedFetch({ [rootId]: [page([node('d1', 'directory')])] })
    const picker = makePicker()

    await picker.open(file, rootId)
    fetchMock.mockClear()
    await picker.loadMore()

    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('surfaces a repeated cursor', async () => {
    routedFetch({
      [rootId]: [
        page([node('d1', 'directory')], 'cursor-2'),
        page([node('d2', 'directory')], 'cursor-2'),
      ],
    })
    const picker = makePicker()

    await picker.open(file, rootId)
    await picker.loadMore()

    expect(picker.error.value).toContain('重复的分页游标')
    expect(picker.loadingMore.value).toBe(false)
  })
})

describe('useMovePicker close', () => {
  it('clears every piece of state', async () => {
    routedFetch({ [rootId]: [page([node('d1', 'directory')], 'cursor-2')] })
    const picker = makePicker()
    await picker.open(file, rootId)
    expect(picker.cursor.value).toBe('cursor-2')

    picker.close()

    expect(picker.target.value).toBeNull()
    expect(picker.path.value).toEqual([])
    expect(picker.directories.value).toEqual([])
    expect(picker.cursor.value).toBeNull()
    expect(picker.error.value).toBe('')
  })
})
