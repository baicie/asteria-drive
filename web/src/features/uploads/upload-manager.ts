/**
 * Browser-to-S3 multipart transport. This module never streams file bytes through the API server.
 * A session can be reconciled after an uncertain completion, and a session that survived a reload
 * can be resumed from the server's read-only part listing (ADR-0027). Resume state itself is the
 * caller's business: this module only consumes an already-validated listing.
 */
import {
  UploadError,
  UploadFailure,
  classifyControlPlaneError,
  classifyFetchError,
  classifyPartResponse,
  isCancellation,
  type UploadCleanupFailure,
  type UploadErrorContext,
  type UploadErrorKind,
} from './upload-errors'
import {
  planResume,
  type ListedPart,
  type UploadPartsListing,
  type UploadResumeHandle,
} from './upload-resume'

export { UploadError, UploadFailure } from './upload-errors'
export type { UploadErrorKind, UploadErrorContext, UploadCleanupFailure } from './upload-errors'

export interface UploadSession {
  id: string
  status: string
  part_size: number
  expected_size: number
}

export interface SignedPart {
  part_number: number
  method: string
  url: string
  required_headers?: Record<string, string>
}

/** A part the client committed to uploading, identified by the hash of its bytes. */
export interface PartChecksum {
  algorithm: 'sha256'
  /** Base64 digest, computed over exactly the bytes of this part. */
  value: string
}

export interface CompletedPart {
  part_number: number
  etag: string
  size: number
}

export interface UploadGateway {
  create(file: File, parentId: string, idempotencyKey: string): Promise<UploadSession>
  sign(uploadId: string, partNumber: number, checksum?: PartChecksum): Promise<SignedPart>
  complete(uploadId: string, parts: CompletedPart[]): Promise<void>
  read(uploadId: string): Promise<UploadSession>
  abort(uploadId: string): Promise<void>
  /** Read-only part reconciliation for resume (ADR-0027). Never creates or mutates a session. */
  parts(uploadId: string): Promise<UploadPartsListing>
}

export interface UploadOptions {
  signal?: AbortSignal
  concurrency?: number
  onProgress?: (percent: number) => void
  onPhase?: (phase: 'uploading' | 'completing') => void
  /** Fired once the control plane has created the session, before any part is sent. */
  onSession?: (session: UploadSession) => void
  /**
   * Computes the content digest sent with each part signature. It is what makes parts in
   * storage provably belong to this file, so resume may skip them. Injectable so tests can
   * stay synchronous; the browser implementation is `webCryptoPartDigest`.
   */
  digest?: (bytes: Blob) => Promise<PartChecksum>
  /**
   * What an interruption means for the server session, evaluated at the moment the
   * transfer actually fails. `cancel` (the default) aborts the multipart upload, because
   * the user asked to give it up. `pause` leaves the session and its durable parts in
   * place so it can be resumed; it only stops the local transfer. It is a function
   * because a pause is decided after this call has already started.
   */
  interruption?: () => 'cancel' | 'pause'
}

/** Explicit, exported bounds so callers and tests can assert on them. */
export const UPLOAD_LIMITS = {
  /** Attempts per part, including the first try. Every attempt re-signs. */
  partAttempts: 4,
  /** Attempts for the completion call, including the first try. */
  completionAttempts: 3,
  /** Upper bound on concurrent part uploads. */
  maxConcurrency: 3,
  /** S3 multipart hard limit. */
  maxParts: 10000,
} as const

export class UploadCancelledError extends UploadError {
  constructor() {
    super('cancelled', '上传已取消', { retryable: false })
    this.name = 'UploadCancelledError'
  }
}

export class UploadCompletionUnknownError extends UploadError {
  readonly uploadId: string

  constructor(uploadId: string) {
    super(
      'completion-unknown',
      '文件提交结果暂时无法确认，请刷新目录或联系管理员核对上传状态，勿直接重复上传',
      { retryable: false, context: { uploadId } },
    )
    this.name = 'UploadCompletionUnknownError'
    this.uploadId = uploadId
  }
}

function ensureNotAborted(signal: AbortSignal) {
  if (signal.aborted) throw new UploadCancelledError()
}

async function pause(ms: number, signal: AbortSignal): Promise<void> {
  ensureNotAborted(signal)
  await new Promise<void>((resolve, reject) => {
    const onAbort = () => {
      clearTimeout(timer)
      reject(new UploadCancelledError())
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

function partError(
  kind: UploadErrorKind,
  partNumber: number,
  message: string,
  extra: Partial<UploadErrorContext> & { retryable?: boolean; cause?: unknown } = {},
): UploadError {
  return new UploadError(kind, message, {
    retryable: extra.retryable ?? false,
    context: { partNumber, ...extra },
    cause: extra.cause,
  })
}

function assertSignedPart(signed: SignedPart, partNumber: number) {
  if (signed.part_number !== partNumber || signed.method !== 'PUT') {
    throw partError('invalid-response', partNumber, '签名返回的分片信息不匹配')
  }
}

/**
 * A deterministic 4xx refusal from the control plane. 408, 429 and 5xx stay retryable:
 * their outcome is genuinely unknown or transient, which is what "completion unknown"
 * is for. 401/403 are refusals too — retrying with the same credentials cannot help.
 */
function isApiRefusal(error: unknown): boolean {
  const status = (error as { status?: unknown } | null)?.status
  if (typeof status !== 'number') return false
  return status >= 400 && status < 500 && status !== 408 && status !== 429
}

async function uploadPart(
  gateway: UploadGateway,
  session: UploadSession,
  file: File,
  index: number,
  signal: AbortSignal,
  options: UploadOptions,
): Promise<CompletedPart> {
  const partNumber = index + 1
  const start = index * session.part_size
  const chunk = file.slice(start, Math.min(start + session.part_size, file.size))
  const maxAttempts = UPLOAD_LIMITS.partAttempts
  let lastError: UploadError | null = null

  // Hash these bytes once, before the attempt loop. Every retry re-signs with the same
  // digest, which is what lets the server recognise the part after a retry instead of
  // treating it as a different upload of the same number.
  const checksum = options.digest ? await options.digest(chunk) : undefined
  ensureNotAborted(signal)

  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    ensureNotAborted(signal)
    let retryable: UploadError | null = null
    try {
      // Always re-sign on retry: an earlier short-lived URL may have expired.
      let signed: SignedPart
      try {
        signed = await gateway.sign(session.id, partNumber, checksum)
      } catch (error) {
        // A failed signature request is a control-plane failure. A 401/403 must not be
        // retried: re-signing will be denied the same way.
        throw classifyControlPlaneError(error)
      }
      ensureNotAborted(signal)
      assertSignedPart(signed, partNumber)

      let response: Response
      try {
        response = await fetch(signed.url, {
          method: signed.method,
          headers: signed.required_headers ?? {},
          body: chunk,
          signal,
        })
      } catch (error) {
        throw classifyFetchError(error)
      }

      if (response.ok) {
        const etag = response.headers.get('ETag')
        if (!etag) {
          throw partError('invalid-response', partNumber, '对象存储未返回 ETag')
        }
        return { part_number: partNumber, etag, size: chunk.size }
      }

      const classified = classifyPartResponse(response.status, await response.text().catch(() => ''))
      const failure = partError(
        classified.kind,
        partNumber,
        classified.retryable ? `HTTP ${response.status}（可重试）` : `HTTP ${response.status}（不可重试）`,
        { retryable: classified.retryable, status: response.status },
      )
      // Authorization and expired-signature failures keep the operator-facing wording.
      const surfaced = classified.kind === 'unauthorized' || classified.kind === 'signature-expired'
        ? partError(classified.kind, partNumber, classified.message, {
          status: response.status,
          retryable: classified.retryable,
        })
        : failure
      if (!classified.retryable) throw surfaced
      // Retryable status: remember it and let the loop decide whether to retry.
      retryable = surfaced
    } catch (error) {
      if (signal.aborted) throw new UploadCancelledError()
      // A part that already carries its own wrapping (thrown above) is re-thrown as is.
      if (error instanceof UploadError && error.message.startsWith(`分片 ${partNumber} 上传`)) throw error
      const classified = error instanceof UploadError ? error : classifyFetchError(error)
      if (!classified.retryable) {
        throw partError(classified.kind, partNumber, `分片 ${partNumber} 上传失败：${classified.message}`, {
          status: classified.context.status,
          attempt,
          maxAttempts,
          cause: classified,
        })
      }
      retryable = classified
    }

    if (retryable) {
      lastError = retryable
      if (attempt === maxAttempts) {
        // Keep the underlying kind (network vs expired signature) so callers can act on
        // it, but mark it non-retryable: the bound has already been reached.
        throw partError(retryable.kind, partNumber, `分片 ${partNumber} 上传重试耗尽（${maxAttempts} 次）：${retryable.message}`, {
          retryable: false,
          attempt,
          maxAttempts,
          cause: retryable,
        })
      }
    }
    await pause(150 * 2 ** (attempt - 1), signal)
  }
  throw lastError ?? partError('non-retryable', partNumber, `分片 ${partNumber} 上传失败`)
}

/**
 * Delete the remote multipart session without letting a cleanup failure replace the
 * primary error. Returns the cleanup failure for the caller to attach.
 */
async function abortQuietly(
  gateway: UploadGateway,
  uploadId: string,
): Promise<UploadCleanupFailure | undefined> {
  try {
    await gateway.abort(uploadId)
    return undefined
  } catch (cause) {
    return { uploadId, cause }
  }
}

function expectedPartSize(fileSize: number, partSize: number, index: number): number {
  return Math.min(partSize, fileSize - index * partSize)
}

function base64(bytes: Uint8Array): string {
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary)
}

/**
 * SHA-256 of one part's bytes, in the base64 form the control plane and S3 both use.
 *
 * Cost: WebCrypto hashes one part slice at a time, off the main thread. A fresh upload
 * hashes every part it sends; a resume hashes only the parts it re-sends. For a 1 GiB file
 * in 8 MiB parts that is 128 hashes of 8 MiB each — roughly a second of CPU in Chromium and
 * no extra network traffic. It is deliberately not a whole-file hash: that would force a
 * second full read before the first byte could be sent, and would not help verify a single
 * resumed part.
 */
export async function webCryptoPartDigest(bytes: Blob): Promise<PartChecksum> {
  const buffer = await bytes.arrayBuffer()
  const digest = await crypto.subtle.digest('SHA-256', buffer)
  return { algorithm: 'sha256', value: base64(new Uint8Array(digest)) }
}

/** The real digest, or a permissive stand-in only when the platform cannot hash. */
function digestFor(options: UploadOptions): (bytes: Blob) => Promise<PartChecksum> {
  if (options.digest) return options.digest
  return webCryptoPartDigest
}

function validateSession(session: UploadSession): number {
  if (!Number.isInteger(session.part_size) || session.part_size < 1) {
    throw new UploadError('invalid-response', '服务端返回了无效的分片大小', {
      context: { uploadId: session.id },
    })
  }
  return session.part_size
}

function partCountFor(fileSize: number, partSize: number, uploadId: string): number {
  const partCount = Math.ceil(fileSize / partSize)
  if (!Number.isSafeInteger(partCount) || partCount < 1 || partCount > UPLOAD_LIMITS.maxParts) {
    throw new UploadError('invalid-response', '文件大小超出单次 Multipart 上传限制', {
      context: { uploadId },
    })
  }
  return partCount
}

/**
 * Fill `completed` with the parts the server already holds, and re-upload the rest. A listed
 * part is only trusted when its number is inside this file's part range and its size matches
 * the slice the current file would produce: a re-selected file with the same name and size but
 * different content at a part boundary cannot corrupt the object, because a mismatch in size
 * (or a later ETag mismatch at completion) fails loudly instead of silently mixing bytes.
 */
async function uploadRemainingParts(
  gateway: UploadGateway,
  session: UploadSession,
  file: File,
  completed: (CompletedPart | undefined)[],
  controller: AbortController,
  options: UploadOptions,
): Promise<void> {
  const partCount = completed.length
  let finished = completed.reduce<number>((count, part) => (part === undefined ? count : count + 1), 0)
  if (finished > 0) options.onProgress?.(Math.round((finished / partCount) * 100))
  const pending = completed.flatMap((part, index) => (part === undefined ? [index] : []))
  const concurrency = Math.max(1, Math.min(UPLOAD_LIMITS.maxConcurrency, Math.floor(options.concurrency ?? UPLOAD_LIMITS.maxConcurrency)))
  let nextPending = 0
  let firstFailure: unknown = null

  await Promise.all(Array.from({ length: Math.min(concurrency, pending.length) }, async () => {
    while (!controller.signal.aborted) {
      const slot = nextPending++
      if (slot >= pending.length) return
      const index = pending[slot]
      try {
        const part = await uploadPart(gateway, session, file, index, controller.signal, options)
        completed[index] = part
        finished += 1
        options.onProgress?.(Math.round((finished / partCount) * 100))
      } catch (error) {
        if (firstFailure === null) firstFailure = error
        controller.abort()
        return
      }
    }
  }))

  if (firstFailure !== null) throw firstFailure
  ensureNotAborted(controller.signal)
}

/**
 * Replay completion with the SAME ordered part list. This is what makes a lost response
 * recoverable: the control plane treats an identical payload as idempotent (ADR-0011), so
 * retrying can only converge on committed.
 */
async function completeWithRetries(
  gateway: UploadGateway,
  uploadId: string,
  completed: (CompletedPart | undefined)[],
  options: UploadOptions,
): Promise<void> {
  const parts: CompletedPart[] = completed.map((part) => {
    if (!part) throw new UploadError('invalid-response', '分片清单不完整，已停止提交', { context: { uploadId } })
    return part
  })
  options.onPhase?.('completing')
  for (let attempt = 0; attempt < UPLOAD_LIMITS.completionAttempts; attempt += 1) {
    try {
      await gateway.complete(uploadId, parts)
      return
    } catch (completeError) {
      let remote: UploadSession | null = null
      try {
        remote = await gateway.read(uploadId)
      } catch {
        remote = null
      }
      if (remote?.status === 'committed') return
      if (remote && ['failed', 'aborted', 'expired'].includes(remote.status)) {
        throw new UploadError('storage-rejected', `上传已终止（服务端状态：${remote.status}）`, {
          context: { uploadId, attempt: attempt + 1 },
          cause: completeError,
        })
      }
      // A 4xx from the control plane is a deterministic refusal, not an unknown outcome:
      // retrying the identical payload cannot change it. 401/403 falls out here too, so
      // an expired login stops immediately instead of reporting "unknown completion".
      if (isApiRefusal(completeError)) {
        throw new UploadError('non-retryable', `文件提交被拒绝：${(completeError as Error).message}`, {
          context: { uploadId, attempt: attempt + 1 },
          cause: completeError,
        })
      }
      if (attempt === UPLOAD_LIMITS.completionAttempts - 1) {
        throw new UploadCompletionUnknownError(uploadId)
      }
      await new Promise<void>((resolve) => setTimeout(resolve, 200 * (attempt + 1)))
    }
  }
}

export async function uploadMultipart(
  gateway: UploadGateway,
  file: File,
  parentId: string,
  idempotencyKey: string,
  options: UploadOptions = {},
): Promise<void> {
  const controller = new AbortController()
  const externalAbort = () => controller.abort()
  options.signal?.addEventListener('abort', externalAbort, { once: true })
  if (options.signal?.aborted) controller.abort()

  let session: UploadSession | undefined
  let completing = false
  try {
    ensureNotAborted(controller.signal)
    try {
      session = await gateway.create(file, parentId, idempotencyKey)
    } catch (error) {
      throw classifyControlPlaneError(error)
    }
    ensureNotAborted(controller.signal)
    const partSize = validateSession(session)
    const partCount = partCountFor(file.size, partSize, session.id)
    options.onSession?.(session)

    options.onPhase?.('uploading')
    const completed = new Array<CompletedPart | undefined>(partCount).fill(undefined)
    await uploadRemainingParts(gateway, session, file, completed, controller, options)

    completing = true
    // Do not abort once completion may have reached the server.
    await completeWithRetries(gateway, session.id, completed, options)
  } catch (error) {
    // The primary error always wins. Cleanup is attempted only when no completion was
    // sent — and never for a pause, whose whole point is to keep the session and its
    // durable parts alive for a later resume. Any cleanup failure is attached as metadata
    // rather than thrown.
    const primary = error instanceof UploadError ? error : classifyFetchError(error)
    const paused = isCancellation(primary) && options.interruption?.() === 'pause'
    if (session && !completing && !paused) {
      const cleanup = await abortQuietly(gateway, session.id)
      if (cleanup) throw new UploadFailure(primary, cleanup)
    }
    throw primary
  } finally {
    options.signal?.removeEventListener('abort', externalAbort)
  }
}

export type ResumeOutcome =
  | { outcome: 'committed' }
  /** `uploadedParts` counts only the parts this resume actually sent; `totalParts` is the file's part count. */
  | { outcome: 'resumed'; uploadedParts: number; totalParts: number }

/**
 * Continue an upload session that outlived a page reload (ADR-0027, ADR-0028).
 *
 * The caller has already read the handle from sessionStorage; this function asks the server
 * what it actually holds and trusts the server's answer, not the handle. No part is re-sent
 * once the session has reached completion: at that point only an idempotent completion replay
 * is safe. A part failure deliberately does NOT abort the session — the whole point of resume
 * is to keep durable parts for the next attempt.
 *
 * Name, size and lastModified are not content identity: two different files can agree on all
 * three. A part is therefore skipped only when the server reports the digest that part was
 * signed with AND hashing the re-selected file's bytes at that offset reproduces it. Every
 * other part is re-uploaded, which costs bandwidth but can never splice another file's bytes
 * into this object.
 */
export async function resumeMultipart(
  gateway: UploadGateway,
  handle: UploadResumeHandle,
  file: File,
  options: UploadOptions = {},
): Promise<ResumeOutcome> {
  if (file.name !== handle.fileName || file.size !== handle.size || file.lastModified !== handle.lastModified) {
    throw new UploadError('non-retryable', '所选文件与待恢复的上传不一致，请重新选择原文件或取消本次恢复', {
      context: { uploadId: handle.uploadId },
    })
  }

  const controller = new AbortController()
  const externalAbort = () => controller.abort()
  options.signal?.addEventListener('abort', externalAbort, { once: true })
  if (options.signal?.aborted) controller.abort()

  try {
    ensureNotAborted(controller.signal)
    let listing: UploadPartsListing
    try {
      listing = await gateway.parts(handle.uploadId)
    } catch (error) {
      throw classifyControlPlaneError(error)
    }
    ensureNotAborted(controller.signal)

    const plan = planResume(listing, handle)
    if (plan.action === 'finished') {
      if (plan.reason === 'already-committed') return { outcome: 'committed' }
      throw new UploadError('storage-rejected', `该上传已结束（服务端状态：${plan.status}），请重新选择文件上传`, {
        context: { uploadId: handle.uploadId },
      })
    }

    if (plan.action === 'complete') {
      // `parts` here is the control plane's frozen completion list. It is the only payload
      // that can be replayed without re-uploading anything.
      options.onPhase?.('completing')
      await completeWithRetries(gateway, handle.uploadId, plan.parts.map(toCompletedPart), options)
      return { outcome: 'committed' }
    }

    const session: UploadSession = {
      id: handle.uploadId,
      status: plan.status,
      // The server's part size is authoritative; the handle's copy is only a UI hint.
      part_size: listing.part_size,
      expected_size: handle.size,
    }
    const partSize = validateSession(session)
    const partCount = partCountFor(file.size, partSize, session.id)
    const completed = new Array<CompletedPart | undefined>(partCount).fill(undefined)
    const verifier = digestFor(options)
    for (const listed of plan.uploaded) {
      const index = listed.part_number - 1
      if (index < 0 || index >= partCount) continue
      if (!listed.etag) continue
      if (listed.size !== expectedPartSize(file.size, partSize, index)) continue
      // No recorded digest means no proof that this part holds the selected file's bytes.
      // Re-uploading it is the only safe answer; skipping it could mix two files.
      ensureNotAborted(controller.signal)
      const proof = await verifier(file.slice(index * partSize, index * partSize + listed.size))
      if (!listed.content_sha256 || proof.value !== listed.content_sha256) continue
      completed[index] = toCompletedPart(listed)
    }

    options.onPhase?.('uploading')
    const beforeSend = completed.reduce((count, part) => (part === undefined ? count : count + 1), 0)
    await uploadRemainingParts(gateway, session, file, completed, controller, options)
    await completeWithRetries(gateway, session.id, completed, options)
    // What this resume actually transmitted: the parts that were still missing at the
    // moment it started sending. Parts verified from storage are not "uploaded parts".
    return { outcome: 'resumed', uploadedParts: partCount - beforeSend, totalParts: partCount }
  } finally {
    options.signal?.removeEventListener('abort', externalAbort)
  }
}

function toCompletedPart(part: ListedPart): CompletedPart {
  return { part_number: part.part_number, etag: part.etag, size: part.size }
}

/** User-facing message for any upload rejection, preserving structured detail when present. */
export function uploadErrorMessage(error: unknown): string {
  if (error instanceof UploadError) return error.message
  if (error instanceof Error) return error.message
  return '上传失败'
}
