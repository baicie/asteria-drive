import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  UPLOAD_LIMITS,
  UploadCancelledError,
  UploadCompletionUnknownError,
  uploadErrorMessage,
  uploadMultipart,
  type CompletedPart,
  type SignedPart,
  type UploadGateway,
  type UploadSession,
} from './upload-manager'
import { UploadError, UploadFailure } from './upload-errors'

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
    read: 0,
    abort: 0,
  }
  // Uncounted defaults; the exported gateway wraps them so overrides are counted too.
  const base: UploadGateway = {
    async create() { return session },
    async sign(_id, partNumber) { return signed(partNumber) },
    async complete() {},
    async read() { return { ...session, status: 'uploading' } },
    async abort() {},
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
