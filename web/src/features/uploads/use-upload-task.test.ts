import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { Node } from '../../api/types'
import { useBusy } from '../../lib/busy'
import {
  RESUME_HANDLE_VERSION,
  RESUME_STORAGE_KEY,
  readResumeHandle,
  writeResumeHandle,
  type ResumeStorage,
  type UploadResumeHandle,
} from './upload-resume'
import { useUploadTask } from './use-upload-task'

/**
 * These tests own the *session lifecycle* around an upload: when a resume handle may
 * survive, when it must be dropped, and which load-time probe is allowed to leave the
 * user a prompt. The transport itself is covered in upload-manager.test.ts; here the
 * file is re-selected through the hook, so every case is also a check that a refusal
 * happens before any storage request is issued.
 */

const NOW = 1_760_000_000_000
const TOKEN = 'test-token'
const PART_SIZE = 5

const rootId = 'root-1'
const root: Node = { id: rootId, kind: 'directory', name: '根目录', revision: 1 }

function memoryStorage(seed: Record<string, string> = {}): ResumeStorage & { data: Map<string, string> } {
  const data = new Map(Object.entries(seed))
  return {
    data,
    getItem: (key) => data.get(key) ?? null,
    setItem: (key, value) => { data.set(key, value) },
    removeItem: (key) => { data.delete(key) },
  }
}

function handle(overrides: Partial<UploadResumeHandle> = {}): UploadResumeHandle {
  return {
    version: RESUME_HANDLE_VERSION,
    uploadId: 'upload-1',
    tenantId: 'tenant-1',
    parentId: rootId,
    parentName: '根目录',
    fileName: 'report.bin',
    size: 10,
    lastModified: NOW,
    partSize: PART_SIZE,
    createdAt: NOW,
    ...overrides,
  }
}

function makeFile(bytes = 'abcdefghij', name = 'report.bin', lastModified = NOW) {
  const file = new File([bytes], name, { type: 'application/octet-stream', lastModified })
  testFileContents.set(file, bytes)
  return file
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

interface Call {
  url: string
  method: string
}

/**
 * A fetch stub driven by a route table. Every unhandled request fails the test: an
 * unexpected request is itself the defect these cases care about.
 */
function installFetch(routes: Record<string, (init: RequestInit) => Response>) {
  const calls: Call[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input)
    const key = Object.keys(routes).find((pattern) => url.includes(pattern))
    calls.push({ url, method: init.method ?? 'GET' })
    if (!key) throw new Error(`unexpected request: ${init.method ?? 'GET'} ${url}`)
    return routes[key](init)
  }))
  return calls
}

function makeTask(storage: ResumeStorage, options: Partial<Parameters<typeof useUploadTask>[0]> = {}) {
  const notices: Array<{ text: string; kind?: string }> = []
  let refreshed = 0
  const busy = useBusy()
  const task = useUploadTask({
    token: ref(TOKEN),
    busy,
    getParent: () => root,
    currentDirectory: ref(root),
    directoryGeneration: () => 0,
    refreshDirectory: async () => { refreshed += 1 },
    notify: (text, kind) => notices.push({ text, kind }),
    clearNotice: () => { notices.push({ text: '', kind: 'clear' }) },
    storage,
    tenantId: () => 'tenant-1',
    isParentVisible: (parentId) => parentId === rootId,
    ...options,
  })
  return {
    task,
    notices,
    busy,
    refreshed: () => refreshed,
  }
}

/** Seed a *live* handle: the reader measures its TTL against the real clock, not NOW. */
function seed(storage: ResumeStorage, value = handle({ createdAt: Date.now() })) {
  writeResumeHandle(storage, value)
}

/**
 * jsdom's Blob.slice returns a stub without arrayBuffer(), so the real WebCrypto digest
 * cannot run there. Parts are sliced from recorded content and hashed deterministically;
 * the resume assertions only need "same bytes → same digest".
 */
const testFileContents = new WeakMap<File, string>()

beforeEach(() => {
  vi.spyOn(File.prototype, 'slice').mockImplementation(function (this: File, start?: number, end?: number) {
    const content = testFileContents.get(this) ?? ''
    const slice = content.slice(start ?? 0, end ?? content.length)
    return {
      size: slice.length,
      text: async () => slice,
      arrayBuffer: async () => {
        const bytes = new TextEncoder().encode(slice)
        return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength)
      },
    } as unknown as Blob
  })
  // The hook hashes parts with WebCrypto; the digest must still distinguish contents.
  vi.stubGlobal('crypto', {
    subtle: {
      digest: async (_algorithm: string, buffer: ArrayBuffer) => {
        const bytes = new Uint8Array(buffer)
        const text = String.fromCharCode(...bytes)
        const hash = new Uint8Array(32)
        for (let index = 0; index < text.length; index += 1) {
          hash[index % 32] = (hash[index % 32] * 33 + text.charCodeAt(index)) % 256
        }
        return hash.buffer
      },
    },
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('resume handle lifecycle on a fresh upload', () => {
  it('writes the handle once the session exists and clears it on success', async () => {
    const storage = memoryStorage()
    let handleDuringUpload: string | null = null
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input)
      if (url.includes('/parts/sign')) {
        const part = JSON.parse(String(init.body)).part_number as number
        return json({ data: { part_number: part, method: 'PUT', url: `https://s3.test/part/${part}`, required_headers: {} } })
      }
      if (url.includes('s3.test/part/')) {
        // The hook must have persisted the handle before the first part is sent.
        handleDuringUpload = storage.data.get(RESUME_STORAGE_KEY) ?? null
        return new Response(null, { status: 200, headers: { ETag: `"etag-${url.split('/').pop()}"` } })
      }
      if (url.includes('/complete')) return json({ data: {} })
      return json({ data: { id: 'upload-1', status: 'created', part_size: PART_SIZE, expected_size: 10 } })
    }))

    const { task } = makeTask(storage)
    await task.start(makeFile())

    expect(handleDuringUpload).toBeTruthy()
    const persisted = JSON.parse(handleDuringUpload!) as UploadResumeHandle
    expect(persisted.uploadId).toBe('upload-1')
    expect(persisted.tenantId).toBe('tenant-1')
    expect(persisted.parentId).toBe(rootId)
    // Success removes the handle: a completed upload must never offer a resume.
    expect(readResumeHandle(storage, NOW)).toBeNull()
  })

  /** A gateway whose S3 PUTs hang until the transfer is aborted. */
  function stallingUpload(storage: ResumeStorage & { data: Map<string, string> }) {
    const deletes: string[] = []
    const abortable = (_input: RequestInfo | URL, init: RequestInit = {}) =>
      new Promise<Response>((_resolve, reject) => {
        init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
      })
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input)
      if (url.includes('/parts/sign')) {
        const part = JSON.parse(String(init.body)).part_number as number
        return json({ data: { part_number: part, method: 'PUT', url: `https://s3.test/part/${part}`, required_headers: {} } })
      }
      if (url.includes('s3.test')) return abortable(input, init)
      if (init.method === 'DELETE') {
        deletes.push(url)
        return new Response(null, { status: 204 })
      }
      return json({ data: { id: 'upload-1', status: 'created', part_size: PART_SIZE, expected_size: 10 } })
    }))
    return {
      storage,
      deletes,
      async pauseAndSettle(task: ReturnType<typeof makeTask>['task']) {
        const started = task.start(makeFile())
        await vi.waitFor(() => expect(storage.data.get(RESUME_STORAGE_KEY)).toBeTruthy())
        task.pause()
        await started
      },
      async cancelAndSettle(task: ReturnType<typeof makeTask>['task']) {
        const started = task.start(makeFile())
        await vi.waitFor(() => expect(storage.data.get(RESUME_STORAGE_KEY)).toBeTruthy())
        task.cancel()
        await started
      },
    }
  }

  it('keeps the handle and the server session after a pause', async () => {
    const storage = memoryStorage()
    const stall = stallingUpload(storage)
    const { task } = makeTask(storage)

    await stall.pauseAndSettle(task)

    expect(task.failureKind.value).toBe('cancelled')
    // Pause exists so the parts survive: no abort may reach the server.
    expect(stall.deletes).toEqual([])
    expect(readResumeHandle(storage, NOW)).toMatchObject({ uploadId: 'upload-1' })
  })

  it('aborts the server session and clears the handle on cancel', async () => {
    const storage = memoryStorage()
    const stall = stallingUpload(storage)
    const { task } = makeTask(storage)

    await stall.cancelAndSettle(task)

    expect(task.failureKind.value).toBe('cancelled')
    // Cancel is the opposite of pause: the multipart upload must be released, and a
    // handle to a session that no longer exists would only offer a resume that 404s.
    expect(stall.deletes).toEqual(['/api/v1/uploads/upload-1'])
    expect(readResumeHandle(storage, NOW)).toBeNull()
  })

  // R3 P1: the same rule on the first upload as on a resume. A cancel that could not reach
  // the server is not a cancel, and the handle is the only pointer to the live session.
  it('keeps the handle and reports an abort failure instead of a clean cancel', async () => {
    const storage = memoryStorage()
    const { task, notices } = makeTask(storage)
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input)
      if (url.includes('/parts/sign')) {
        const part = JSON.parse(String(init.body)).part_number as number
        return json({ data: { part_number: part, method: 'PUT', url: `https://s3.test/part/${part}`, required_headers: {} } })
      }
      if (url.includes('s3.test')) {
        return new Promise<Response>((_resolve, reject) => {
          init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
        })
      }
      if (init.method === 'DELETE') throw new TypeError('control plane unreachable')
      return json({ data: { id: 'upload-1', status: 'created', part_size: PART_SIZE, expected_size: 10 } })
    }))

    const started = task.start(makeFile())
    await vi.waitFor(() => expect(storage.data.get(RESUME_STORAGE_KEY)).toBeTruthy())
    task.cancel()
    await started

    expect(notices.some((entry) => entry.kind === 'error' && entry.text.includes('未能通知服务端'))).toBe(true)
    expect(notices.some((entry) => entry.text.includes('已取消'))).toBe(false)
    expect(readResumeHandle(storage, NOW)).toMatchObject({ uploadId: 'upload-1' })
  })

  // R3.1 (A): the automatic abort after a plain network failure can itself fail. The
  // session may then still hold durable parts, so the handle must survive.
  it('keeps the handle when a non-cancel failure cannot be cleaned up', async () => {
    const storage = memoryStorage()
    const { task, notices } = makeTask(storage)
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input)
      if (url.includes('/parts/sign')) {
        const part = JSON.parse(String(init.body)).part_number as number
        return json({ data: { part_number: part, method: 'PUT', url: `https://s3.test/part/${part}`, required_headers: {} } })
      }
      if (url.includes('s3.test')) throw new TypeError('Failed to fetch')
      if (init.method === 'DELETE') throw new TypeError('control plane unreachable')
      return json({ data: { id: 'upload-1', status: 'created', part_size: PART_SIZE, expected_size: 10 } })
    }))

    await task.start(makeFile())

    expect(task.failureKind.value).toBe('network')
    // Primary failure wording survives; the unconfirmed cleanup is appended, never replacing it.
    const errorNotice = notices.find((entry) => entry.kind === 'error')
    expect(errorNotice?.text).toContain('网络')
    expect(errorNotice?.text).toContain('远端状态尚未确认')
    expect(readResumeHandle(storage, NOW)).toMatchObject({ uploadId: 'upload-1' })
  })

  it('drops the handle when a non-cancel failure is cleaned up successfully', async () => {
    const storage = memoryStorage()
    const { task, notices } = makeTask(storage)
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input)
      if (url.includes('/parts/sign')) {
        const part = JSON.parse(String(init.body)).part_number as number
        return json({ data: { part_number: part, method: 'PUT', url: `https://s3.test/part/${part}`, required_headers: {} } })
      }
      if (url.includes('s3.test')) throw new TypeError('Failed to fetch')
      if (init.method === 'DELETE') return new Response(null, { status: 204 })
      return json({ data: { id: 'upload-1', status: 'created', part_size: PART_SIZE, expected_size: 10 } })
    }))

    await task.start(makeFile())

    expect(task.failureKind.value).toBe('network')
    expect(notices.some((entry) => entry.kind === 'error' && entry.text.includes('远端状态尚未确认'))).toBe(false)
    // The session is gone, so a handle would only offer a resume that 404s.
    expect(readResumeHandle(storage, NOW)).toBeNull()
  })

  it('drops the handle when the server refuses the upload for good', async () => {
    const storage = memoryStorage()
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/parts/sign')) {
        return json({ data: { part_number: 1, method: 'PUT', url: 'https://s3.test/part/1', required_headers: {} } })
      }
      if (url.includes('s3.test')) return new Response('AccessDenied', { status: 403 })
      if (url.includes('/api/v1/uploads/upload-1')) return new Response(null, { status: 204 })
      return json({ data: { id: 'upload-1', status: 'created', part_size: PART_SIZE, expected_size: 10 } })
    }))

    const { task } = makeTask(storage)
    await task.start(makeFile('abcde'))

    expect(task.failureKind.value).toBe('unauthorized')
    expect(readResumeHandle(storage, NOW)).toBeNull()
  })
})

describe('load-time resume probe', () => {
  it('offers a resume for a live session', async () => {
    const storage = memoryStorage()
    seed(storage)
    const calls = installFetch({
      '/api/v1/uploads/upload-1': () => json({ data: { id: 'upload-1', status: 'uploading', part_size: PART_SIZE, expected_size: 10 } }),
    })

    const { task } = makeTask(storage)
    await task.discoverResume()

    expect(task.pendingResume.value).toMatchObject({ status: 'uploading', handle: { uploadId: 'upload-1' } })
    expect(calls).toHaveLength(1)
    expect(readResumeHandle(storage, NOW)).not.toBeNull()
  })

  it.each([[400], [401], [403], [404]])('discards the handle on a %i probe response', async (status) => {
    const storage = memoryStorage()
    seed(storage)
    installFetch({
      '/api/v1/uploads/upload-1': () => json({ error: { message: 'nope' } }, status),
    })

    const { task, notices } = makeTask(storage)
    await task.discoverResume()

    expect(task.pendingResume.value).toBeNull()
    expect(readResumeHandle(storage, NOW)).toBeNull()
    // A stale handle is not the user's problem; only a real session state is worth a notice.
    expect(notices.filter((entry) => entry.kind === 'error')).toEqual([])
  })

  it.each([[500], [503]])('keeps the handle after a transient %i probe response', async (status) => {
    const storage = memoryStorage()
    seed(storage)
    installFetch({
      '/api/v1/uploads/upload-1': () => json({ error: { message: 'busy' } }, status),
    })

    const { task } = makeTask(storage)
    await task.discoverResume()

    expect(task.pendingResume.value).toBeNull()
    // An offline or overloaded server must not cost the user their resumable session.
    expect(readResumeHandle(storage, NOW)).not.toBeNull()
  })

  it('keeps the handle when the probe cannot reach the network at all', async () => {
    const storage = memoryStorage()
    seed(storage)
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))

    const { task, notices } = makeTask(storage)
    await task.discoverResume()

    expect(task.pendingResume.value).toBeNull()
    expect(readResumeHandle(storage, NOW)).not.toBeNull()
    expect(notices.filter((entry) => entry.kind === 'error')).toEqual([])
  })

  it('reports a committed session and refreshes the visible parent', async () => {
    const storage = memoryStorage()
    seed(storage)
    installFetch({
      '/api/v1/uploads/upload-1': () => json({ data: { id: 'upload-1', status: 'committed', part_size: PART_SIZE, expected_size: 10 } }),
    })

    const { task, notices, refreshed } = makeTask(storage)
    await task.discoverResume()

    expect(task.pendingResume.value).toBeNull()
    expect(readResumeHandle(storage, NOW)).toBeNull()
    // A committed session is informational, not a failure: `notify` defaults to success.
    expect(notices.some((entry) => entry.kind === undefined && entry.text.includes('report.bin'))).toBe(true)
    expect(refreshed()).toBe(1)
  })

  it('does not refresh a directory the upload was not made in', async () => {
    const storage = memoryStorage()
    seed(storage, handle({ parentId: 'other-parent' }))
    installFetch({
      '/api/v1/uploads/upload-1': () => json({ data: { id: 'upload-1', status: 'committed', part_size: PART_SIZE, expected_size: 10 } }),
    })

    const { task, refreshed } = makeTask(storage)
    await task.discoverResume()

    expect(readResumeHandle(storage, NOW)).toBeNull()
    expect(refreshed()).toBe(0)
  })

  it.each(['aborted', 'expired', 'failed'])('reports a %s session as ended', async (status) => {
    const storage = memoryStorage()
    seed(storage)
    installFetch({
      '/api/v1/uploads/upload-1': () => json({ data: { id: 'upload-1', status, part_size: PART_SIZE, expected_size: 10 } }),
    })

    const { task, notices } = makeTask(storage)
    await task.discoverResume()

    expect(task.pendingResume.value).toBeNull()
    expect(readResumeHandle(storage, NOW)).toBeNull()
    expect(notices.some((entry) => entry.kind === 'error' && entry.text.includes(status))).toBe(true)
  })

  it('drops a handle whose status this client does not understand', async () => {
    const storage = memoryStorage()
    seed(storage)
    installFetch({
      '/api/v1/uploads/upload-1': () => json({ data: { id: 'upload-1', status: 'paused-for-review', part_size: PART_SIZE, expected_size: 10 } }),
    })

    const { task } = makeTask(storage)
    await task.discoverResume()

    // Guessing at an unknown state risks completing the wrong upload.
    expect(task.pendingResume.value).toBeNull()
    expect(readResumeHandle(storage, NOW)).toBeNull()
  })

  it('makes no request at all when there is no handle', async () => {
    const storage = memoryStorage()
    const calls = installFetch({})
    const { task } = makeTask(storage)

    await task.discoverResume()

    expect(calls).toEqual([])
    expect(task.pendingResume.value).toBeNull()
  })

  it('discards an expired handle without a probe', async () => {
    const storage = memoryStorage()
    storage.setItem(RESUME_STORAGE_KEY, JSON.stringify(handle({ createdAt: Date.now() - 25 * 60 * 60 * 1000 })))
    const calls = installFetch({})

    const { task } = makeTask(storage)
    await task.discoverResume()

    expect(calls).toEqual([])
    expect(storage.data.size).toBe(0)
  })

  it('runs only one probe for two concurrent calls', async () => {
    const storage = memoryStorage()
    seed(storage)
    let releases = 0
    vi.stubGlobal('fetch', vi.fn(async () => {
      releases += 1
      await new Promise<void>((resolve) => setTimeout(resolve, 0))
      return json({ data: { id: 'upload-1', status: 'uploading', part_size: PART_SIZE, expected_size: 10 } })
    }))
    const { task } = makeTask(storage)

    await Promise.all([task.discoverResume(), task.discoverResume()])

    // React's mount effects can double-fire; the probe must not be issued twice.
    expect(releases).toBe(1)
    expect(task.pendingResume.value).not.toBeNull()
  })
})

describe('resuming from the banner', () => {
  async function primePending(task: ReturnType<typeof makeTask>['task'], storage: ResumeStorage) {
    seed(storage)
    installFetch({
      '/api/v1/uploads/upload-1': () => json({ data: { id: 'upload-1', status: 'uploading', part_size: PART_SIZE, expected_size: 10 } }),
    })
    await task.discoverResume()
    expect(task.pendingResume.value).not.toBeNull()
  }

  it('refuses a file the handle was not created for, before any request', async () => {
    const storage = memoryStorage()
    const { task, notices } = makeTask(storage)
    await primePending(task, storage)

    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      calls.push(String(input))
      return json({ data: null })
    }))

    await task.resume(makeFile('abcdefghij', 'other.bin'))

    expect(calls).toEqual([])
    expect(task.failureKind.value).toBe('non-retryable')
    expect(task.pendingResume.value).toBeNull()
    expect(readResumeHandle(storage, NOW)).toBeNull()
    expect(notices.some((entry) => entry.kind === 'error' && entry.text.includes('不一致'))).toBe(true)
  })

  it('re-uploads a part with no integrity evidence and clears everything on success', async () => {
    const storage = memoryStorage()
    const { task, notices, refreshed } = makeTask(storage)
    await primePending(task, storage)

    const signed: number[] = []
    let completion: unknown = null
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input)
      if (url.endsWith('/parts') && (init.method ?? 'GET') === 'GET') {
        // The server reports part 1 but holds no signed digest for it, so this client
        // cannot prove those bytes belong to the selected file and must re-send them.
        return json({ data: { upload_id: 'upload-1', status: 'uploading', part_size: PART_SIZE, parts: [{ part_number: 1, etag: '"stale"', size: 5 }], complete: true } })
      }
      if (url.includes('/parts/sign')) {
        const part = JSON.parse(String(init.body)).part_number as number
        signed.push(part)
        return json({ data: { part_number: part, method: 'PUT', url: `https://s3.test/part/${part}`, required_headers: {} } })
      }
      if (url.includes('s3.test')) return new Response(null, { status: 200, headers: { ETag: `"etag-${url.split('/').pop()}"` } })
      if (url.includes('/complete')) {
        completion = JSON.parse(String(init.body))
        return json({ data: {} })
      }
      return json({ data: null })
    }))

    const outcome = await task.resume(makeFile())

    expect(signed).toEqual([1, 2])
    // The completion list carries the freshly uploaded ETags, not the untrusted ones.
    expect(completion).toMatchObject({
      parts: [{ part_number: 1, etag: '"etag-1"' }, { part_number: 2, etag: '"etag-2"' }],
    })
    expect(outcome).toEqual({ outcome: 'resumed', uploadedParts: 2, totalParts: 2 })
    expect(task.pendingResume.value).toBeNull()
    expect(readResumeHandle(storage, NOW)).toBeNull()
    expect(refreshed()).toBe(1)
    // The success notice must survive the refresh: loadDirectory clears the notice when it
    // starts, so notifying first would erase the only confirmation the user gets.
    expect(notices.some((entry) => entry.text.includes('已恢复并完成'))).toBe(true)
  })

  it('refuses a second resume while one is in flight', async () => {
    const storage = memoryStorage()
    const { task } = makeTask(storage)
    await primePending(task, storage)

    let partRequests = 0
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input)
      if (url.endsWith('/parts') && (init.method ?? 'GET') === 'GET') {
        return Promise.resolve(json({ data: { upload_id: 'upload-1', status: 'uploading', part_size: PART_SIZE, parts: [], complete: true } }))
      }
      if (url.includes('/parts/sign')) {
        const part = JSON.parse(String(init.body)).part_number as number
        return Promise.resolve(json({ data: { part_number: part, method: 'PUT', url: `https://s3.test/part/${part}`, required_headers: {} } }))
      }
      if (url.includes('s3.test')) {
        partRequests += 1
        return new Promise<Response>((resolve) => setTimeout(() => resolve(new Response(null, { status: 200, headers: { ETag: `"e${url.split('/').pop()}"` } })), 5))
      }
      return Promise.resolve(json({ data: {} }))
    }))

    const first = task.resume(makeFile())
    const second = await task.resume(makeFile())
    await first

    // The second call is refused up front: exactly one reconciliation, and each of the
    // two parts is sent exactly once.
    expect(second).toBeNull()
    expect(partRequests).toBe(2)
  })

  it('drops the offer when the session was terminated in the meantime', async () => {
    const storage = memoryStorage()
    const { task, notices } = makeTask(storage)
    await primePending(task, storage)

    vi.stubGlobal('fetch', vi.fn(async () =>
      json({ data: { upload_id: 'upload-1', status: 'expired', part_size: PART_SIZE, parts: [], complete: true } })))

    expect(await task.resume(makeFile())).toBeNull()
    expect(task.pendingResume.value).toBeNull()
    expect(task.failureKind.value).toBe('storage-rejected')
    expect(readResumeHandle(storage, NOW)).toBeNull()
    expect(notices.some((entry) => entry.kind === 'error')).toBe(true)
  })

  it('keeps the offer after a transient part failure so a second try is possible', async () => {
    const storage = memoryStorage()
    const { task } = makeTask(storage)
    await primePending(task, storage)

    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input)
      if (url.endsWith('/parts') && (init.method ?? 'GET') === 'GET') {
        return json({ data: { upload_id: 'upload-1', status: 'uploading', part_size: PART_SIZE, parts: [], complete: true } })
      }
      if (url.includes('/parts/sign')) {
        const part = JSON.parse(String(init.body)).part_number as number
        return json({ data: { part_number: part, method: 'PUT', url: `https://s3.test/part/${part}`, required_headers: {} } })
      }
      if (url.includes('s3.test')) throw new TypeError('Failed to fetch')
      return json({ data: null })
    }))

    expect(await task.resume(makeFile())).toBeNull()

    expect(task.failureKind.value).toBe('network')
    // A network loss is not a reason to throw away durable parts.
    expect(task.pendingResume.value).not.toBeNull()
    expect(readResumeHandle(storage, NOW)).not.toBeNull()
  })

  // R3 P1: the R2 hook aborted only its local controller, so a cancel during a resume left
  // the server's multipart upload alive. The DELETE is the observable fact that matters.
  it('cancels a resume by telling the server to abort, then drops the offer', async () => {
    const storage = memoryStorage()
    const { task, notices } = makeTask(storage)
    await primePending(task, storage)

    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    const deletes: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input)
      if (url.endsWith('/parts') && (init.method ?? 'GET') === 'GET') {
        return json({ data: { upload_id: 'upload-1', status: 'uploading', part_size: PART_SIZE, parts: [], complete: true } })
      }
      if (url.includes('/parts/sign')) {
        const part = JSON.parse(String(init.body)).part_number as number
        return json({ data: { part_number: part, method: 'PUT', url: `https://s3.test/part/${part}`, required_headers: {} } })
      }
      if (url.includes('s3.test')) {
        // Stall the transfer so the cancel happens while it is genuinely in flight.
        await gate
        return new Response(null, { status: 200, headers: { ETag: '"part"' } })
      }
      if (url.endsWith('/api/v1/uploads/upload-1') && init.method === 'DELETE') {
        deletes.push(url)
        return new Response(null, { status: 204 })
      }
      return json({ data: null })
    }))

    const resuming = task.resume(makeFile())
    await vi.waitFor(() => expect(task.phase.value).toBe('uploading'))
    task.cancel()
    release()
    await resuming

    expect(deletes).toHaveLength(1)
    expect(task.pendingResume.value).toBeNull()
    expect(readResumeHandle(storage, NOW)).toBeNull()
    expect(notices.some((entry) => entry.text.includes('已取消'))).toBe(true)
    expect(task.failureKind.value).toBe('cancelled')
  })

  it('keeps the offer and reports the failure when the abort itself fails', async () => {
    const storage = memoryStorage()
    const { task, notices } = makeTask(storage)
    await primePending(task, storage)

    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input)
      if (url.endsWith('/parts') && (init.method ?? 'GET') === 'GET') {
        return json({ data: { upload_id: 'upload-1', status: 'uploading', part_size: PART_SIZE, parts: [], complete: true } })
      }
      if (url.includes('/parts/sign')) {
        const part = JSON.parse(String(init.body)).part_number as number
        return json({ data: { part_number: part, method: 'PUT', url: `https://s3.test/part/${part}`, required_headers: {} } })
      }
      if (url.includes('s3.test')) {
        await gate
        return new Response(null, { status: 200, headers: { ETag: '"part"' } })
      }
      if (url.endsWith('/api/v1/uploads/upload-1') && init.method === 'DELETE') {
        throw new TypeError('control plane unreachable')
      }
      return json({ data: null })
    }))

    const resuming = task.resume(makeFile())
    await vi.waitFor(() => expect(task.phase.value).toBe('uploading'))
    task.cancel()
    release()
    await resuming

    // The user must not be told the upload is gone when the server was never told.
    expect(notices.some((entry) => entry.kind === 'error' && entry.text.includes('未能通知服务端'))).toBe(true)
    expect(notices.some((entry) => entry.text.includes('已取消'))).toBe(false)
    // The session may still exist, so the offer and handle stay for verification.
    expect(task.pendingResume.value).not.toBeNull()
    expect(readResumeHandle(storage, NOW)).not.toBeNull()
  })

  it('ignores a resume when there is no pending session', async () => {
    const storage = memoryStorage()
    const calls = installFetch({})
    const { task } = makeTask(storage)

    expect(await task.resume(makeFile())).toBeNull()
    expect(calls).toEqual([])
  })

  it('dismissing the offer clears the stored handle', async () => {
    const storage = memoryStorage()
    const { task } = makeTask(storage)
    await primePending(task, storage)

    task.dismissResume()

    expect(task.pendingResume.value).toBeNull()
    expect(readResumeHandle(storage, NOW)).toBeNull()
  })
})

describe('logout', () => {
  it('clears the pending offer and the stored handle', async () => {
    const storage = memoryStorage()
    const { task, busy } = makeTask(storage)
    seed(storage)
    installFetch({
      '/api/v1/uploads/upload-1': () => json({ data: { id: 'upload-1', status: 'uploading', part_size: PART_SIZE, expected_size: 10 } }),
    })
    await task.discoverResume()
    expect(task.pendingResume.value).not.toBeNull()

    task.cancelForLogout()

    expect(task.pendingResume.value).toBeNull()
    // A handle belongs to the signed-in identity and must not outlive it.
    expect(readResumeHandle(storage, NOW)).toBeNull()
    // No upload was in flight, so logout must not decrement another operation's count.
    expect(busy.any.value).toBe(false)
  })
})
