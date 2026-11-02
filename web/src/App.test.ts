import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App as VueApp } from 'vue'
import App from './App.vue'

const rootId = 'root-1'
const fileA = { id: 'file-a', kind: 'file', name: 'a.txt', size: 1, revision: 1, parent_id: rootId }
const fileB = { id: 'file-b', kind: 'file', name: 'b.txt', size: 1, revision: 1, parent_id: rootId }

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

function flush() {
  return new Promise<void>((resolve) => setTimeout(resolve, 0))
}

interface Deferred {
  url: string
  resolve: () => void
}

let mounted: VueApp | null = null
let authRequests: Deferred[] = []
let nodeDeletes: Deferred[] = []
let openCalls = 0

function installFetchStub() {
  authRequests = []
  nodeDeletes = []
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.includes('/tenant')) return Promise.resolve(json({ data: { display_name: 'T', root_directory_id: rootId } }))
    if (url.endsWith(`/directories/${rootId}`)) return Promise.resolve(json({ data: { id: rootId, kind: 'directory', name: '根目录', revision: 1 } }))
    if (url.includes(`/directories/${rootId}/children`)) return Promise.resolve(json({ data: [fileA, fileB], page: { next_cursor: null } }))
    if (init?.method === 'DELETE') {
      return new Promise<Response>((resolve) => {
        nodeDeletes.push({ url, resolve: () => resolve(new Response(null, { status: 204 })) })
      })
    }
    if (url.includes('/download-authorizations')) {
      return new Promise<Response>((resolve) => {
        authRequests.push({ url, resolve: () => resolve(json({ data: { url: 'https://signed.example/x' } })) })
      })
    }
    return Promise.resolve(json({ data: null }))
  }))
}

function installWindowOpenStub() {
  openCalls = 0
  vi.stubGlobal('open', vi.fn(() => {
    openCalls += 1
    return { closed: false, opener: null, location: { replace: () => {} }, close: () => {} }
  }))
}

async function mountApp() {
  localStorage.setItem('asteria.token', 'test-token')
  const container = document.createElement('div')
  document.body.appendChild(container)
  mounted = createApp(App)
  mounted.mount(container)
  await flush()
  await flush()
  await nextTick()
  return container
}

/** Row action links, in template order: 下载/打开, 重命名, 移动, 回收. */
function actionLink(container: HTMLElement, row: number, index: number) {
  return container.querySelectorAll('.file-row')[row].querySelectorAll('.row-action .action-link')[index] as HTMLButtonElement
}

afterEach(() => {
  mounted?.unmount()
  mounted = null
  document.body.innerHTML = ''
  localStorage.clear()
  vi.unstubAllGlobals()
})

describe('concurrent operations', () => {
  it('keeps a second download disabled after the first one settles', async () => {
    installFetchStub()
    installWindowOpenStub()
    const container = await mountApp()

    actionLink(container, 0, 0).click()
    await nextTick()
    expect(authRequests).toHaveLength(1)
    expect(actionLink(container, 0, 0).disabled).toBe(true)

    // Start download B while A's authorization is still pending.
    actionLink(container, 1, 0).click()
    await nextTick()
    expect(authRequests).toHaveLength(2)
    expect(actionLink(container, 1, 0).disabled).toBe(true)

    // Settle A while B is still in flight.
    authRequests[0].resolve()
    await flush()
    await nextTick()

    // B must still look busy: the old shared `busy` string was cleared by A here and
    // re-enabled B, which let a second click fire a duplicate authorization + popup.
    expect(actionLink(container, 1, 0).disabled).toBe(true)
    expect(actionLink(container, 1, 0).textContent?.trim()).toBe('准备中…')

    actionLink(container, 1, 0).click()
    await nextTick()
    expect(openCalls).toBe(2)
    expect(authRequests).toHaveLength(2)

    authRequests[1].resolve()
    await flush()
    await nextTick()
    expect(actionLink(container, 1, 0).disabled).toBe(false)
  })

  it('does not re-enable a pending recycle when an unrelated download settles', async () => {
    installFetchStub()
    installWindowOpenStub()
    vi.stubGlobal('confirm', vi.fn(() => true))
    const container = await mountApp()

    actionLink(container, 0, 0).click()
    await nextTick()
    expect(authRequests).toHaveLength(1)

    const recycleB = actionLink(container, 1, 3)
    recycleB.click()
    await nextTick()
    expect(nodeDeletes).toHaveLength(1)
    expect(recycleB.disabled).toBe(true)

    authRequests[0].resolve()
    await flush()
    await flush()
    await nextTick()

    expect(recycleB.disabled).toBe(true)
    recycleB.click()
    await flush()
    expect(nodeDeletes).toHaveLength(1)

    nodeDeletes[0].resolve()
    await flush()
    await flush()
  })

  it('refuses a duplicate recycle click while the first DELETE is in flight', async () => {
    installFetchStub()
    installWindowOpenStub()
    vi.stubGlobal('confirm', vi.fn(() => true))
    const container = await mountApp()

    const recycleA = actionLink(container, 0, 3)
    recycleA.click()
    recycleA.click()
    await nextTick()

    expect(nodeDeletes).toHaveLength(1)
    nodeDeletes[0].resolve()
    await flush()
  })
})
