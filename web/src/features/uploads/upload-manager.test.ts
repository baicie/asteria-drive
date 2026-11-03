import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  UPLOAD_LIMITS,
  UploadCancelledError,
  UploadCompletionUnknownError,
  resumeMultipart,
  uploadErrorMessage,
  uploadMultipart,
  type CompletedPart,
  type PartChecksum,
  type SignedPart,
  type UploadGateway,
  type UploadSession,
} from './upload-manager'
import { UploadError, UploadFailure } from './upload-errors'
import type { ListedPart, UploadPartsListing, UploadResumeHandle } from './upload-resume'

const session: UploadSession = { id: 'upload-1', status: 'created', part_size: 5, expected_size: 10 }

function makeFile(bytes = 'abcdefghij') {
  return new File([bytes], 'file.txt', { type: 'text/plain' })
}

function jsonResponse(status: number, body?: unknown, headers: Record<string, string> = {}) {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  })
}

function signed(partNumber: number): SignedPart {
  return { part_number: partNumber, method: 'PUT', url: `https://s3.test/part/${partNumber}`, required_headers: {} }
}

/** A gateway whose S3 PUTs are served by a caller-supplied fetch stub. */
function makeGateway(overrides: Partial<UploadGateway> = {}) {
  const calls = {
    create: 0,
    sign: [] as number[],
    complete: 0,
    completeParts: [] as CompletedPart[][],
    read: 0,
    abort: 0,
    parts: [] as string[],
  }
  // Uncounted defaults; the exported gateway wraps them so overrides are counted too.
  const base: UploadGateway = {
    async create() { return session },
    async sign(_id, partNumber) { return signed(partNumber) },
    async complete() {},
    async read() { return { ...session, status: 'uploading' } },
    async abort() {},
    async parts() {
      return { upload_id: session.id, status: 'uploading', part_size: session.part_size, parts: [], complete: true }
    },
  }
  const gateway: UploadGateway = {
    create: (file, parent, key) => {
      calls.create += 1
      return (overrides.create ?? base.create)(file, parent, key)
    },
    sign: (id, partNumber) => {
      calls.sign.push(partNumber)
      return (overrides.sign ?? base.sign)(id, partNumber)
    },
    complete: (id, parts) => {
      calls.complete += 1
      calls.completeParts.push(parts)
      return (overrides.complete ?? base.complete)(id, parts)
    },
    read: (id) => {
      calls.read += 1
      return (overrides.read ?? base.read)(id)
    },
    abort: (id) => {
      calls.abort += 1
      return (overrides.abort ?? base.abort)(id)
    },
    parts: (id) => {
      calls.parts.push(id)
      return (overrides.parts ?? base.parts)(id)
    },
  }
  return { gateway, calls }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('uploadMultipart error taxonomy', () => {
  it('retries a transient PUT with a fresh signature and reports success', async () => {
    const attempts = new Map<number, number>()
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const part = Number(String(url).split('/').pop())
      const count = (attempts.get(part) ?? 0) + 1
      attempts.set(part, count)
      if (part === 2 && count === 1) return new Response('busy', { status: 503 })
      return new Response(null, { status: 200, headers: { ETag: `"etag-${part}"` } })
    }))
    const { gateway, calls } = makeGateway()

    await uploadMultipart(gateway, makeFile(), 'parent', 'key')

    expect(calls.sign).toEqual([1, 2, 2])
    expect(calls.complete).toBe(1)
    expect(calls.abort).toBe(0)
    expect(attempts.get(2)).toBe(2)
  })

  it('classifies a non-retryable PUT status and surfaces the part number', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('nope', { status: 404 })))
    const { gateway, calls } = makeGateway()

    // One part only: concurrency would otherwise sign the second part before the first fails.
    const error = await uploadMultipart(gateway, makeFile('abcde'), 'parent', 'key').catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('non-retryable')
    expect(error.context.partNumber).toBe(1)
    expect(error.context.status).toBe(404)
    expect(error.message).toBe('分片 1 上传失败：HTTP 404（不可重试）')
    expect(calls.sign).toEqual([1])
    expect(calls.complete).toBe(0)
    expect(calls.abort).toBe(1)
  })

  it('retries an expired S3 signature instead of treating the 403 as a denial', async () => {
    let attempts = 0
    vi.stubGlobal('fetch', vi.fn(async () => {
      attempts += 1
      // Mimic SeaweedFS/S3: an expired presigned URL is a 403 with an explanatory body.
      if (attempts === 1) {
        return new Response('<Error><Code>AccessDenied</Code><Message>Request has expired</Message></Error>', { status: 403 })
      }
      return new Response(null, { status: 200, headers: { ETag: '"etag-1"' } })
    }))
    const { gateway, calls } = makeGateway()

    await expect(uploadMultipart(gateway, makeFile('abcde'), 'parent', 'key')).resolves.toBeUndefined()

    expect(attempts).toBe(2)
    expect(calls.sign.length).toBe(2)
    expect(calls.complete).toBe(1)
  })

  it('surfaces exhausted signature expiry with its own kind after the bound', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('Request has expired', { status: 403 })))
    const { gateway, calls } = makeGateway()

    const error = await uploadMultipart(gateway, makeFile('abcde'), 'parent', 'key').catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('signature-expired')
    expect(error.retryable).toBe(false)
    expect(error.message).toContain('重试耗尽')
    expect(calls.sign.length).toBe(UPLOAD_LIMITS.partAttempts)
  })

  it('does not retry a control-plane 403 from the signing endpoint', async () => {
    const { gateway, calls } = makeGateway({
      async sign() {
        throw Object.assign(new Error('forbidden'), { name: 'ApiError', status: 403 })
      },
    })

    const error = await uploadMultipart(gateway, makeFile('abcde'), 'parent', 'key').catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('unauthorized')
    expect(error.retryable).toBe(false)
    // One attempt only: re-signing a denied request cannot succeed.
    expect(calls.sign).toEqual([1])
    expect(calls.abort).toBe(1)
  })

  it('does not retry a control-plane 404 from the create endpoint', async () => {
    const { gateway, calls } = makeGateway({
      async create() {
        throw Object.assign(new Error('parent missing'), { name: 'ApiError', status: 404 })
      },
    })

    const error = await uploadMultipart(gateway, makeFile('abcde'), 'parent', 'key').catch((e) => e)

    expect(error.kind).toBe('non-retryable')
    expect(error.retryable).toBe(false)
    expect(error.context.status).toBe(404)
    expect(calls.create).toBe(1)
  })

  it('classifies a 403 as an authorization failure rather than a retryable network error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('denied', { status: 403 })))
    const { gateway } = makeGateway()

    const error = await uploadMultipart(gateway, makeFile(), 'parent', 'key').catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('unauthorized')
    expect(error.retryable).toBe(false)
  })

  it('recognises an expired signature as unauthorized and does not retry forever', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('expired', { status: 401 })))
    const { gateway, calls } = makeGateway()

    const error = await uploadMultipart(gateway, makeFile(), 'parent', 'key').catch((e) => e)

    expect(error.kind).toBe('unauthorized')
    expect(calls.sign.length).toBeLessThanOrEqual(UPLOAD_LIMITS.partAttempts)
  })

  it('bounds part retries at UPLOAD_LIMITS.partAttempts', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('down', { status: 500 })))
    const { gateway, calls } = makeGateway()

    const error = await uploadMultipart(gateway, makeFile('abcde'), 'parent', 'key').catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('network')
    expect(error.message).toContain('重试耗尽')
    expect(calls.sign.length).toBe(UPLOAD_LIMITS.partAttempts)
  })

  it('treats a fetch rejection as a retryable network error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
    const { gateway, calls } = makeGateway()

    const error = await uploadMultipart(gateway, makeFile('abcde'), 'parent', 'key').catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('network')
    expect(error.context.maxAttempts).toBe(UPLOAD_LIMITS.partAttempts)
    expect(calls.sign.length).toBe(UPLOAD_LIMITS.partAttempts)
  })

  it('rejects a signature whose part number does not match as an invalid response', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"x"' } })))
    const { gateway } = makeGateway({
      async sign() {
        return { part_number: 99, method: 'PUT', url: 'https://s3.test/part/1' }
      },
    })

    const error = await uploadMultipart(gateway, makeFile(), 'parent', 'key').catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('invalid-response')
    expect(error.message).toContain('不匹配')
  })

  it('aborts with a cancellation error and never completes', async () => {
    const controller = new AbortController()
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init: RequestInit) => {
      // Abort while the first part is in flight.
      setTimeout(() => controller.abort(), 0)
      await new Promise((resolve, reject) => {
        init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
        setTimeout(resolve, 1000)
      })
      return new Response(null, { status: 200, headers: { ETag: '"1"' } })
    }))
    const { gateway, calls } = makeGateway()

    const error = await uploadMultipart(gateway, makeFile(), 'parent', 'key', { signal: controller.signal })
      .catch((e) => e)

    expect(error).toBeInstanceOf(UploadCancelledError)
    expect(error.kind).toBe('cancelled')
    expect(calls.complete).toBe(0)
  })

  it('does not let an abort failure replace the primary upload error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('nope', { status: 404 })))
    const cleanupCause = new Error('cleanup endpoint unavailable')
    const { gateway } = makeGateway({
      async abort() {
        throw cleanupCause
      },
    })

    const error = await uploadMultipart(gateway, makeFile(), 'parent', 'key').catch((e) => e)

    // The primary cause survives; cleanup is attached as metadata, not thrown.
    expect(error).toBeInstanceOf(UploadFailure)
    expect(error.kind).toBe('non-retryable')
    expect(error.message).toBe('分片 1 上传失败：HTTP 404（不可重试）')
    expect(error.cleanup).toEqual({ uploadId: session.id, cause: cleanupCause })
  })

  it('surfaces a cleanup failure on the primary error without masking it', async () => {
    const controller = new AbortController()
    controller.abort()
    const { gateway } = makeGateway({
      async abort() {
        throw new Error('abort failed')
      },
    })

    const error = await uploadMultipart(gateway, makeFile(), 'parent', 'key', { signal: controller.signal })
      .catch((e) => e)

    expect(error.kind).toBe('cancelled')
  })
})

describe('uploadMultipart completion semantics', () => {
  it('returns success when a lost completion response is reconciled as committed', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"1"' } })))
    let reads = 0
    const { gateway, calls } = makeGateway({
      async complete() {
        throw new Error('network reset')
      },
      async read() {
        reads += 1
        return { ...session, status: 'committed' }
      },
    })

    await expect(uploadMultipart(gateway, makeFile(), 'parent', 'key')).resolves.toBeUndefined()
    expect(reads).toBe(1)
    expect(calls.abort).toBe(0)
  })

  it('fails fast on a deterministic 4xx completion refusal instead of reporting an unknown outcome', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"1"' } })))
    const { gateway, calls } = makeGateway({
      async complete() {
        throw Object.assign(new Error('completion payload differs'), { name: 'ApiError', status: 409 })
      },
      async read() {
        return { ...session, status: 'uploading' }
      },
    })

    const error = await uploadMultipart(gateway, makeFile('abcde'), 'parent', 'key').catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('non-retryable')
    expect(error.message).toContain('文件提交被拒绝')
    expect(calls.complete).toBe(1)
    // Completion already reached the server, so the session is never aborted.
    expect(calls.abort).toBe(0)
  })

  it('reports an unknown completion outcome after the bounded attempts and never aborts', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"1"' } })))
    const { gateway, calls } = makeGateway({
      async complete() {
        throw new Error('network reset')
      },
      async read() {
        return { ...session, status: 'uploading' }
      },
    })

    const error = await uploadMultipart(gateway, makeFile(), 'parent', 'key').catch((e) => e)

    expect(error).toBeInstanceOf(UploadCompletionUnknownError)
    expect(error.kind).toBe('completion-unknown')
    expect(error.uploadId).toBe(session.id)
    expect(calls.complete).toBe(UPLOAD_LIMITS.completionAttempts)
    expect(calls.read).toBe(UPLOAD_LIMITS.completionAttempts)
    expect(calls.abort).toBe(0)
  })

  it('fails fast when the server reports the upload was terminated', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"1"' } })))
    const { gateway, calls } = makeGateway({
      async complete() {
        throw new Error('conflict')
      },
      async read() {
        return { ...session, status: 'aborted' }
      },
    })

    const error = await uploadMultipart(gateway, makeFile(), 'parent', 'key').catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('storage-rejected')
    expect(error.message).toContain('aborted')
    expect(calls.complete).toBe(1)
    expect(calls.abort).toBe(0)
  })

  it('replays completion when the first response is lost after object_completed', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"1"' } })))
    let completes = 0
    let readStatus = 'object_completed'
    const { gateway } = makeGateway({
      async complete() {
        completes += 1
        if (completes === 1) throw new Error('lost response')
        readStatus = 'committed'
      },
      async read() {
        return { ...session, status: readStatus }
      },
    })

    const parts: CompletedPart[] = []
    await expect(uploadMultipart(gateway, makeFile(), 'parent', 'key')).resolves.toBeUndefined()
    expect(completes).toBe(2)
    expect(parts).toEqual([])
  })
})

describe('uploadMultipart server contract validation', () => {
  it('rejects a non-integer part size from the control plane', async () => {
    const { gateway } = makeGateway({
      async create() {
        return { ...session, part_size: 0 }
      },
    })

    const error = await uploadMultipart(gateway, makeFile(), 'parent', 'key').catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('invalid-response')
    expect(error.message).toContain('分片大小')
  })

  it('rejects a file that would exceed the multipart part limit', async () => {
    const { gateway } = makeGateway({
      async create() {
        return { ...session, part_size: 1 }
      },
    })

    // 10001 parts: the check must fire before any part is signed.
    const error = await uploadMultipart(gateway, makeFile('x'.repeat(UPLOAD_LIMITS.maxParts + 1)), 'parent', 'key')
      .catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('invalid-response')
    expect(error.message).toContain('Multipart')
  })

  it('surfaces the upload id on an invalid server response after session creation', async () => {
    const { gateway } = makeGateway({
      async create() {
        return { ...session, part_size: -1 }
      },
    })

    const error = await uploadMultipart(gateway, makeFile(), 'parent', 'key').catch((e) => e)

    expect(error.context.uploadId).toBe(session.id)
  })
})

describe('uploadErrorMessage', () => {
  it('uses the structured message and falls back for unknown values', () => {
    expect(uploadErrorMessage(new UploadError('network', '网络错误'))).toBe('网络错误')
    expect(uploadErrorMessage(new Error('plain'))).toBe('plain')
    expect(uploadErrorMessage('nonsense')).toBe('上传失败')
  })
})

/** Content of each test file, because jsdom's Blob.slice cannot read its own bytes back. */
const fileContents = new WeakMap<File, string>()

function resumeFile(bytes = 'abcdefghij', lastModified = 1_760_000_000_000) {
  const file = new File([bytes], 'file.txt', { type: 'text/plain', lastModified })
  fileContents.set(file, bytes)
  return file
}

// jsdom's Blob.slice returns a stub with neither arrayBuffer() nor readable text, so a
// real digest over a slice cannot run there. Slicing the recorded content keeps the
// property under test honest: two files with the same name/size but different bytes hash
// differently, which is exactly the mixed-content case.
beforeEach(() => {
  vi.spyOn(File.prototype, 'slice').mockImplementation(function (this: File, start?: number, end?: number) {
    const content = fileContents.get(this) ?? ''
    const slice = content.slice(start ?? 0, end ?? content.length)
    return { size: slice.length, text: async () => slice } as unknown as Blob
  })
})

function makeHandle(file: File, overrides: Partial<UploadResumeHandle> = {}): UploadResumeHandle {
  return {
    version: 1,
    uploadId: 'upload-1',
    tenantId: 'tenant-1',
    parentId: 'parent-1',
    parentName: '根目录',
    fileName: file.name,
    size: file.size,
    lastModified: file.lastModified,
    partSize: 5,
    createdAt: 1_760_000_000_000,
    ...overrides,
  }
}

function listing(overrides: Partial<UploadPartsListing> = {}): UploadPartsListing {
  return {
    upload_id: 'upload-1',
    status: 'uploading',
    part_size: 5,
    parts: [],
    complete: true,
    ...overrides,
  }
}

/**
 * A deterministic content digest for tests. Real hashing uses WebCrypto, which jsdom's
 * Blob does not fully implement; the resume contract only needs "same bytes → same
 * digest, different bytes → different digest", which this provides.
 */
const testDigest = async (bytes: Blob): Promise<PartChecksum> => {
  // The shim in beforeEach exposes only `text()`; a real Blob would expose `text()` too.
  const text = typeof (bytes as { text?: unknown }).text === 'function'
    ? await bytes.text()
    : ''
  return { algorithm: 'sha256', value: `test:${text.length}:${text}` }
}

/** A listed part whose digest matches the bytes the test file holds at that offset. */
function signedPart(partNumber: number, bytes: string, etag = `"etag-${partNumber}"`): ListedPart {
  return {
    part_number: partNumber,
    etag,
    size: bytes.length,
    content_sha256: `test:${bytes.length}:${bytes}`,
  }
}

describe('resumeMultipart', () => {
  it('re-uploads only the missing part and completes with the reconciled list', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"etag-2"' } })))
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        // Part 1 is signed with the digest of the bytes this file holds at that offset.
        return listing({ parts: [signedPart(1, 'abcde')] })
      },
    })

    const result = await resumeMultipart(gateway, makeHandle(file), file, { digest: testDigest })

    expect(result).toEqual({ outcome: 'resumed', uploadedParts: 1, totalParts: 2 })
    expect(calls.parts).toEqual(['upload-1'])
    expect(calls.sign).toEqual([2])
    expect(calls.complete).toBe(1)
    expect(calls.completeParts[0]).toEqual([
      { part_number: 1, etag: '"etag-1"', size: 5 },
      { part_number: 2, etag: '"etag-2"', size: 5 },
    ])
    // Resume must never call create or abort: the session already exists.
    expect(calls.create).toBe(0)
    expect(calls.abort).toBe(0)
  })

  it('re-uploads a listed part when the selected file holds different bytes (P0 mixed content)', async () => {
    // The attack the round-2 review asks about: file B has the same name, size and
    // lastModified as A but different content. Part 1 of A is already in storage.
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"fresh-1"' } })))
    const fileA = resumeFile('AAAAABBBBB')
    const fileB = resumeFile('XXXXXBBBBB')
    expect([fileB.name, fileB.size, fileB.lastModified]).toEqual([fileA.name, fileA.size, fileA.lastModified])
    const handle = makeHandle(fileA)
    const { gateway, calls } = makeGateway({
      async parts() {
        // The server reports the digest A's part 1 was signed with.
        return listing({ parts: [signedPart(1, 'AAAAA')] })
      },
    })

    const result = await resumeMultipart(gateway, handle, fileB, { digest: testDigest })

    // B's part 1 hashes differently, so it must be re-sent rather than reused.
    expect(calls.sign).toEqual([1, 2])
    expect(result).toEqual({ outcome: 'resumed', uploadedParts: 2, totalParts: 2 })
    // Both parts were re-sent, so both ETags come from the fresh PUT.
    expect(calls.completeParts[0]).toEqual([
      { part_number: 1, etag: '"fresh-1"', size: 5 },
      { part_number: 2, etag: '"fresh-1"', size: 5 },
    ])
  })

  it('re-uploads a part the server has no integrity evidence for', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"fresh-1"' } })))
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        // No content_sha256: the server cannot prove these bytes belong to this file.
        return listing({ parts: [{ part_number: 1, etag: '"etag-1"', size: 5 }] })
      },
    })

    await resumeMultipart(gateway, makeHandle(file), file, { digest: testDigest })

    expect(calls.sign).toEqual([1, 2])
    expect(calls.completeParts[0][0]).toEqual({ part_number: 1, etag: '"fresh-1"', size: 5 })
  })

  it('verifies parts by digest even when part order in the listing is reversed', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"x"' } })))
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        return listing({ parts: [signedPart(2, 'fghij'), signedPart(1, 'abcde')] })
      },
    })

    await resumeMultipart(gateway, makeHandle(file), file, { digest: testDigest })

    expect(calls.sign).toEqual([])
    expect(calls.completeParts[0].map((part) => part.part_number)).toEqual([1, 2])
  })

  it('completes immediately without uploading when the session was completing', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"x"' } })))
    const file = resumeFile()
    const frozen = [
      { part_number: 1, etag: '"etag-1"', size: 5 },
      { part_number: 2, etag: '"etag-2"', size: 5 },
    ]
    const { gateway, calls } = makeGateway({
      async parts() {
        return listing({ status: 'completing', parts: frozen })
      },
    })

    const result = await resumeMultipart(gateway, makeHandle(file), file)

    expect(result).toEqual({ outcome: 'committed' })
    expect(calls.sign).toEqual([])
    expect(calls.completeParts).toEqual([frozen])
  })

  it('reports a committed session without touching storage again', async () => {
    vi.stubGlobal('fetch', vi.fn())
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        return listing({ status: 'committed', parts: [{ part_number: 1, etag: '"a"', size: 5 }] })
      },
    })

    await expect(resumeMultipart(gateway, makeHandle(file), file)).resolves.toEqual({ outcome: 'committed' })
    expect(calls.sign).toEqual([])
    expect(calls.complete).toBe(0)
  })

  it('refuses a terminated session with a structured error', async () => {
    const file = resumeFile()
    for (const status of ['aborted', 'expired', 'failed']) {
      const { gateway, calls } = makeGateway({
        async parts() {
          return listing({ status })
        },
      })
      const error = await resumeMultipart(gateway, makeHandle(file), file).catch((e) => e)
      expect(error).toBeInstanceOf(UploadError)
      expect(error.kind).toBe('storage-rejected')
      expect(calls.complete).toBe(0)
      expect(calls.abort).toBe(0)
    }
  })

  it('rejects a re-selected file that does not match the handle before any request', async () => {
    const original = resumeFile('abcdefghij', 1_760_000_000_000)
    const different = resumeFile('abcdefghij', 1_760_000_000_001)
    const { gateway, calls } = makeGateway()

    const error = await resumeMultipart(gateway, makeHandle(original), different).catch((e) => e)

    expect(error.kind).toBe('non-retryable')
    expect(calls.parts).toEqual([])
    expect(calls.sign).toEqual([])
  })

  it('re-uploads every part when the listing is truncated', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const part = String(url).split('/').pop()
      return new Response(null, { status: 200, headers: { ETag: `"etag-${part}"` } })
    }))
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        // complete=false means the server could not prove this is the whole set.
        return listing({ complete: false, parts: [signedPart(1, 'abcde')] })
      },
    })

    await resumeMultipart(gateway, makeHandle(file), file, { digest: testDigest })

    expect(calls.sign).toEqual([1, 2])
  })

  it('does not trust a listed part whose size contradicts this file', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"fresh"' } })))
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        return listing({ parts: [{ part_number: 1, etag: '"stale"', size: 4, content_sha256: 'test:4:abcd' }] })
      },
    })

    await resumeMultipart(gateway, makeHandle(file), file, { digest: testDigest })

    expect(calls.sign).toEqual([1, 2])
    expect(calls.completeParts[0][0]).toEqual({ part_number: 1, etag: '"fresh"', size: 5 })
  })

  it('ignores listed parts outside the current file range', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"fresh"' } })))
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        return listing({ parts: [
          signedPart(1, 'abcde'),
          signedPart(2, 'fghij'),
          signedPart(9, 'zzzzz', '"etag-9"'),
        ] })
      },
    })

    await resumeMultipart(gateway, makeHandle(file), file, { digest: testDigest })

    expect(calls.sign).toEqual([])
    expect(calls.completeParts[0]).toHaveLength(2)
  })

  it('keeps the session when a part fails mid-resume instead of aborting durable parts', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('nope', { status: 404 })))
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        return listing({ parts: [signedPart(1, 'abcde')] })
      },
    })

    const error = await resumeMultipart(gateway, makeHandle(file), file, { digest: testDigest }).catch((e) => e)

    expect(error).toBeInstanceOf(UploadError)
    expect(error.kind).toBe('non-retryable')
    expect(calls.complete).toBe(0)
    // Aborting here would delete the parts that were just reconciled, making the next
    // resume start from zero — the opposite of what resume is for.
    expect(calls.abort).toBe(0)
  })

  it('rejects a listing for a different session id', async () => {
    vi.stubGlobal('fetch', vi.fn())
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        return listing({ upload_id: 'someone-else' })
      },
    })

    const error = await resumeMultipart(gateway, makeHandle(file), file).catch((e) => e)

    expect(error.kind).toBe('storage-rejected')
    expect(calls.complete).toBe(0)
  })

  it('classifies an unreadable listing as a control-plane failure', async () => {
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        throw Object.assign(new Error('forbidden'), { name: 'ApiError', status: 403 })
      },
    })

    const error = await resumeMultipart(gateway, makeHandle(file), file).catch((e) => e)

    expect(error.kind).toBe('unauthorized')
    expect(calls.sign).toEqual([])
  })

  it('cancels a resume without aborting the session', async () => {
    const controller = new AbortController()
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        controller.abort()
        return listing({ parts: [signedPart(1, 'abcde')] })
      },
    })

    const error = await resumeMultipart(gateway, makeHandle(file), file, { signal: controller.signal, digest: testDigest })
      .catch((e) => e)

    expect(error).toBeInstanceOf(UploadCancelledError)
    expect(calls.sign).toEqual([])
    expect(calls.abort).toBe(0)
  })

  it('reports an unknown completion after the bounded attempts and never aborts', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 200, headers: { ETag: '"1"' } })))
    const file = resumeFile()
    const { gateway, calls } = makeGateway({
      async parts() {
        return listing({
          parts: [signedPart(1, 'abcde'), signedPart(2, 'fghij')],
        })
      },
      async complete() {
        throw new Error('network reset')
      },
      async read() {
        return { ...session, status: 'uploading' }
      },
    })

    const error = await resumeMultipart(gateway, makeHandle(file), file, { digest: testDigest }).catch((e) => e)

    expect(error).toBeInstanceOf(UploadCompletionUnknownError)
    expect(calls.complete).toBe(UPLOAD_LIMITS.completionAttempts)
    expect(calls.abort).toBe(0)
  })
})
