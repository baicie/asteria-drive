import { describe, expect, it, vi } from 'vitest'
import {
  RESUME_HANDLE_VERSION,
  RESUME_STORAGE_KEY,
  classifyProbeStatus,
  clearResumeHandle,
  isResumable,
  isTerminal,
  parseResumeHandle,
  planResume,
  readResumeHandle,
  writeResumeHandle,
  type ResumeStorage,
  type UploadPartsListing,
  type UploadResumeHandle,
} from './upload-resume'

const NOW = 1_760_000_000_000

function handle(overrides: Partial<UploadResumeHandle> = {}): UploadResumeHandle {
  return {
    version: 1,
    uploadId: 'upload-1',
    tenantId: 'tenant-1',
    parentId: 'parent-1',
    parentName: '根目录',
    fileName: 'report.bin',
    size: 10,
    lastModified: NOW - 1000,
    partSize: 5,
    createdAt: NOW,
    ...overrides,
  }
}

function listing(overrides: Partial<UploadPartsListing> = {}): UploadPartsListing {
  return {
    upload_id: 'upload-1',
    status: 'uploading',
    part_size: 5,
    parts: [{ part_number: 1, etag: '"etag-1"', size: 5, storage_sha256: 'sha', reusable: true }],
    complete: true,
    ...overrides,
  }
}

/** In-memory stand-in for sessionStorage, matching only the three methods we use. */
function memoryStorage(seed: Record<string, string> = {}): ResumeStorage & { data: Map<string, string> } {
  const data = new Map(Object.entries(seed))
  return {
    data,
    getItem: (key) => data.get(key) ?? null,
    setItem: (key, value) => { data.set(key, value) },
    removeItem: (key) => { data.delete(key) },
  }
}

describe('resume handle persistence', () => {
  it('round-trips a handle and never stores credentials', () => {
    const storage = memoryStorage()
    writeResumeHandle(storage, handle())

    const stored = storage.data.get(RESUME_STORAGE_KEY)
    expect(stored).toBeTruthy()
    // The brief forbids persisting presigned URLs, tokens or S3 credentials. Assert on the
    // raw serialized form so a future field cannot slip in unnoticed.
    expect(stored).not.toMatch(/https?:\/\//)
    expect(stored).not.toMatch(/authorization/i)
    expect(stored).not.toMatch(/x-amz-/i)
    expect(stored).not.toMatch(/token/i)
    expect(stored).not.toMatch(/secret/i)
    expect(stored).not.toMatch(/credential/i)

    expect(readResumeHandle(storage, NOW)).toEqual(handle())
  })

  it('drops a handle older than the upload TTL without a server call', () => {
    const storage = memoryStorage()
    writeResumeHandle(storage, handle())
    const stale = NOW + 25 * 60 * 60 * 1000
    expect(readResumeHandle(storage, stale)).toBeNull()
  })

  it('rejects malformed JSON instead of repairing it', () => {
    for (const raw of ['{', 'null', '[]', '"handle"']) {
      const verdict = parseResumeHandle(raw, NOW)
      expect(verdict.ok).toBe(false)
    }
  })

  it('rejects a foreign handle version', () => {
    const verdict = parseResumeHandle(JSON.stringify({ ...handle(), version: 99 }), NOW)
    expect(verdict).toEqual({ ok: false, reason: 'version' })
  })

  it('rejects a handle with a missing or invalid field', () => {
    const broken: Partial<UploadResumeHandle>[] = [
      { uploadId: '' },
      { parentId: '' },
      { fileName: '' },
      { size: 0 },
      { size: -1 },
      { partSize: 0 },
      { lastModified: Number.NaN },
      { createdAt: 0 },
      { parentName: 'x'.repeat(256) },
    ]
    for (const overrides of broken) {
      const verdict = parseResumeHandle(JSON.stringify({ ...handle(), ...overrides }), NOW)
      expect(verdict.ok, JSON.stringify(overrides)).toBe(false)
    }
  })

  it('accepts an empty tenantId because it is diagnostic, never authorization', () => {
    // The server scopes every upload read to the authenticated tenant, so the recorded
    // tenant id cannot grant access. Requiring it would silently disable resume for any
    // build that does not know the tenant id, which is exactly the regression this pins.
    const verdict = parseResumeHandle(JSON.stringify({ ...handle(), tenantId: '' }), NOW)
    expect(verdict.ok).toBe(true)
    if (verdict.ok) expect(verdict.handle.uploadId).toBe('upload-1')
  })

  it('still rejects a tenantId of the wrong type or an absurd length', () => {
    for (const tenantId of [7, null, 'x'.repeat(65)]) {
      const verdict = parseResumeHandle(JSON.stringify({ ...handle(), tenantId }), NOW)
      expect(verdict.ok, String(tenantId)).toBe(false)
    }
  })

  it('distinguishes a missing handle from a corrupt one', () => {
    expect(parseResumeHandle(null, NOW)).toEqual({ ok: false, reason: 'missing' })
    expect(parseResumeHandle('{oops', NOW)).toEqual({ ok: false, reason: 'malformed' })
  })

  it('clears a handle idempotently', () => {
    const storage = memoryStorage()
    writeResumeHandle(storage, handle())
    clearResumeHandle(storage)
    clearResumeHandle(storage)
    expect(storage.data.size).toBe(0)
  })

  it('survives a storage implementation that throws', () => {
    const hostile: ResumeStorage = {
      getItem() { throw new Error('denied') },
      setItem() { throw new Error('denied') },
      removeItem() { throw new Error('denied') },
    }
    // Private-mode storage and quota errors must never fail the upload itself.
    expect(() => writeResumeHandle(hostile, handle())).not.toThrow()
    expect(() => clearResumeHandle(hostile)).not.toThrow()
    expect(readResumeHandle(hostile, NOW)).toBeNull()
  })

  it('uses a versioned key so an old build cannot read a new payload', () => {
    expect(RESUME_STORAGE_KEY).toBe('asteria.upload.resume')
    expect(RESUME_HANDLE_VERSION).toBe(1)
  })
})

describe('status classification', () => {
  it('treats only live states as resumable', () => {
    for (const status of ['created', 'uploading', 'completing', 'object_completed']) {
      expect(isResumable(status)).toBe(true)
      expect(isTerminal(status)).toBe(false)
    }
    for (const status of ['committed', 'aborted', 'expired', 'failed']) {
      expect(isResumable(status)).toBe(false)
      expect(isTerminal(status)).toBe(true)
    }
  })

  it('classifies a committed session separately from a terminated one', () => {
    expect(classifyProbeStatus('committed')).toBe('committed')
    expect(classifyProbeStatus('aborted')).toBe('terminated')
    expect(classifyProbeStatus('expired')).toBe('terminated')
    expect(classifyProbeStatus('failed')).toBe('terminated')
    expect(classifyProbeStatus('uploading')).toBe('resumable')
    // An unknown status from a newer server is never guessed at.
    expect(classifyProbeStatus('paused-for-review')).toBe('unknown')
  })
})

describe('planResume', () => {
  it('resumes a live session with the server-listed parts', () => {
    const plan = planResume(listing(), handle())
    expect(plan).toEqual({
      action: 'resume',
      uploadId: 'upload-1',
      uploaded: listing().parts,
      status: 'uploading',
    })
  })

  it('ignores a truncated listing rather than treating it as the complete set', () => {
    const plan = planResume(listing({ complete: false }), handle())
    expect(plan).toMatchObject({ action: 'resume', uploaded: [] })
  })

  it('replays completion without uploading when the session is past the part phase', () => {
    for (const status of ['completing', 'object_completed']) {
      const plan = planResume(listing({ status }), handle())
      expect(plan).toEqual({
        action: 'complete',
        uploadId: 'upload-1',
        parts: listing().parts,
      })
    }
  })

  it('reports a committed session as finished', () => {
    expect(planResume(listing({ status: 'committed' }), handle())).toMatchObject({
      action: 'finished',
      reason: 'already-committed',
    })
  })

  it('reports a terminated session as finished', () => {
    for (const status of ['aborted', 'expired', 'failed']) {
      expect(planResume(listing({ status }), handle())).toMatchObject({
        action: 'finished',
        reason: 'terminated',
      })
    }
  })

  it('refuses a listing that belongs to another session', () => {
    const plan = planResume(listing({ upload_id: 'upload-2' }), handle())
    expect(plan).toMatchObject({ action: 'finished', reason: 'mismatched-session' })
  })
})

describe('storage safety', () => {
  it('does not touch window.sessionStorage when a storage is injected', () => {
    const spy = vi.spyOn(window.sessionStorage, 'setItem')
    const storage = memoryStorage()
    writeResumeHandle(storage, handle())
    writeResumeHandle(storage, handle({ uploadId: 'upload-2' }))
    expect(spy).not.toHaveBeenCalled()
    spy.mockRestore()
  })
})
