/**
 * Cross-reload upload resume (ADR-0027).
 *
 * The only thing this module ever persists is a non-sensitive session handle: upload id,
 * tenant id, parent id, file identity, declared size and part size. It never writes a
 * presigned URL, an Authorization header, an ETag map, or any S3 credential. The handle is
 * not a credential: every resume re-authenticates with the current bearer token, and the
 * part list is re-read from the server each time.
 *
 * Storage is `sessionStorage`, not `localStorage`: resume means "this tab reloaded", not
 * "this browser keeps state across users and days". Logout clears it.
 *
 * Browsers expose no way to re-obtain the original `File` after a reload, so the resumed
 * upload needs the file re-selected. That is deliberate: keeping file bytes in storage
 * would be a far larger privacy and quota problem than the resume is worth.
 */

export const RESUME_STORAGE_KEY = 'asteria.upload.resume'
export const RESUME_HANDLE_VERSION = 1

export interface UploadResumeHandle {
  version: 1
  uploadId: string
  /**
   * Diagnostic only. It is never used to authorize anything: the server scopes every read
   * to the authenticated tenant, so a stale or empty value can at worst cost one 404.
   * Kept because it makes a handle left by another identity obvious during diagnosis.
   */
  tenantId: string
  parentId: string
  parentName: string
  fileName: string
  size: number
  lastModified: number
  partSize: number
  /** Wall-clock creation time, so a stale handle can be discarded without a server call. */
  createdAt: number
}

export interface ListedPart {
  part_number: number
  etag: string
  size: number
}

export interface UploadPartsListing {
  upload_id: string
  status: string
  part_size: number
  parts: ListedPart[]
  complete: boolean
}

/** Minimal storage surface so tests can supply a fake without touching jsdom quirks. */
export interface ResumeStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

export type ResumeHandleVerdict =
  | { ok: true; handle: UploadResumeHandle }
  | { ok: false; reason: 'missing' | 'malformed' | 'version' }

function isPositiveInt(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

function isNonEmptyString(value: unknown, max: number): value is string {
  return typeof value === 'string' && value.length > 0 && value.length <= max
}

/**
 * Validate an unparsed handle. A corrupted or foreign version is dropped rather than
 * repaired: guessing at resume state is how a client ends up completing the wrong upload.
 */
export function parseResumeHandle(raw: string | null, now = Date.now(), ttlMs = 24 * 60 * 60 * 1000): ResumeHandleVerdict {
  if (!raw) return { ok: false, reason: 'missing' }
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return { ok: false, reason: 'malformed' }
  }
  if (!parsed || typeof parsed !== 'object') return { ok: false, reason: 'malformed' }
  const value = parsed as Record<string, unknown>
  if (value.version !== RESUME_HANDLE_VERSION) return { ok: false, reason: 'version' }
  const valid = isNonEmptyString(value.uploadId, 64) &&
    // `tenantId` is diagnostic, not authoritative: an empty value must not invalidate an
    // otherwise usable handle, or a build that does not know the tenant id could never
    // offer a resume at all. The length cap still keeps a corrupt value bounded.
    typeof value.tenantId === 'string' && value.tenantId.length <= 64 &&
    isNonEmptyString(value.parentId, 64) &&
    typeof value.parentName === 'string' && value.parentName.length <= 255 &&
    isNonEmptyString(value.fileName, 255) &&
    isPositiveInt(value.size) &&
    typeof value.lastModified === 'number' && Number.isFinite(value.lastModified) &&
    isPositiveInt(value.partSize) &&
    typeof value.createdAt === 'number' && Number.isFinite(value.createdAt)
  if (!valid) return { ok: false, reason: 'malformed' }
  // A handle older than the upload TTL cannot refer to a live session; skip the request.
  if (now - (value.createdAt as number) > ttlMs) return { ok: false, reason: 'malformed' }
  return { ok: true, handle: value as unknown as UploadResumeHandle }
}

export function readResumeHandle(storage: ResumeStorage, now = Date.now()): UploadResumeHandle | null {
  const verdict = parseResumeHandle(safeGet(storage, RESUME_STORAGE_KEY), now)
  return verdict.ok ? verdict.handle : null
}

export function writeResumeHandle(storage: ResumeStorage, handle: UploadResumeHandle): void {
  try {
    storage.setItem(RESUME_STORAGE_KEY, JSON.stringify(handle))
  } catch {
    // Storage can be disabled (private mode, quota). Resume is best-effort: continue the
    // upload without it rather than failing a transfer over optional bookkeeping.
  }
}

export function clearResumeHandle(storage: ResumeStorage): void {
  try {
    storage.removeItem(RESUME_STORAGE_KEY)
  } catch {
    // Same rationale as writeResumeHandle.
  }
}

function safeGet(storage: ResumeStorage, key: string): string | null {
  try {
    return storage.getItem(key)
  } catch {
    return null
  }
}

/**
 * Decide whether a server-reported session can be resumed, and with which parts.
 *
 * `complete === false` means the listing was truncated, so it cannot be trusted as the set
 * of uploaded parts; the caller must fall back to uploading every part again rather than
 * assume the listed ones are all there are.
 */
export type ResumePlan =
  | { action: 'resume'; uploadId: string; uploaded: ListedPart[]; status: string }
  | { action: 'complete'; uploadId: string; parts: ListedPart[] }
  | { action: 'finished'; uploadId: string; status: string; reason: string }

export function planResume(listing: UploadPartsListing, handle: UploadResumeHandle): ResumePlan {
  if (listing.upload_id !== handle.uploadId) {
    // A response for another session must never be applied to this handle.
    return { action: 'finished', uploadId: handle.uploadId, status: listing.status, reason: 'mismatched-session' }
  }
  if (listing.status === 'committed') {
    return { action: 'finished', uploadId: handle.uploadId, status: listing.status, reason: 'already-committed' }
  }
  if (['failed', 'aborted', 'expired'].includes(listing.status)) {
    return { action: 'finished', uploadId: handle.uploadId, status: listing.status, reason: 'terminated' }
  }
  if (listing.status === 'completing' || listing.status === 'object_completed') {
    // Never resend parts once completion may have reached the server (ADR-0011). The
    // recorded completion list is the only payload that can be replayed safely.
    return { action: 'complete', uploadId: handle.uploadId, parts: listing.parts }
  }
  if (!listing.complete) {
    return { action: 'resume', uploadId: handle.uploadId, uploaded: [], status: listing.status }
  }
  return { action: 'resume', uploadId: handle.uploadId, uploaded: listing.parts, status: listing.status }
}

export function isResumable(status: string): boolean {
  return status === 'created' || status === 'uploading' || status === 'completing' || status === 'object_completed'
}

/** True when this status means the session is gone and the handle must be dropped. */
export function isTerminal(status: string): boolean {
  return status === 'committed' || status === 'failed' || status === 'aborted' || status === 'expired'
}

/**
 * Classify the session status returned by the load-time probe. Kept separate from the HTTP
 * call so the decision table can be read and tested without any I/O:
 *
 * - `resumable`  — offer the user a resume;
 * - `committed`  — the upload already finished; drop the handle and refresh the directory;
 * - `terminated` — aborted/expired/failed; drop the handle and tell the user why;
 * - `unknown`    — a status this client does not understand (a newer server, say); drop the
 *                  handle rather than guess, since guessing risks completing the wrong thing.
 */
export type ProbeClassification = 'resumable' | 'committed' | 'terminated' | 'unknown'

export function classifyProbeStatus(status: string): ProbeClassification {
  if (status === 'committed') return 'committed'
  if (isTerminal(status)) return 'terminated'
  if (isResumable(status)) return 'resumable'
  return 'unknown'
}
