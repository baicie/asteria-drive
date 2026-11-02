/**
 * Browser-to-S3 multipart transport. This module never streams file bytes through the API server.
 * A session can be reconciled after an uncertain completion; cross-reload part resume is NOT supported
 * (it needs a server-side ListParts endpoint, which does not exist yet — see ADR-0027).
 */
import {
  UploadError,
  UploadFailure,
  classifyControlPlaneError,
  classifyFetchError,
  classifyPartResponse,
  type UploadCleanupFailure,
  type UploadErrorContext,
  type UploadErrorKind,
} from './upload-errors'

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

export interface CompletedPart {
  part_number: number
  etag: string
  size: number
}

export interface UploadGateway {
  create(file: File, parentId: string, idempotencyKey: string): Promise<UploadSession>
  sign(uploadId: string, partNumber: number): Promise<SignedPart>
  complete(uploadId: string, parts: CompletedPart[]): Promise<void>
  read(uploadId: string): Promise<UploadSession>
  abort(uploadId: string): Promise<void>
}

export interface UploadOptions {
  signal?: AbortSignal
  concurrency?: number
  onProgress?: (percent: number) => void
  onPhase?: (phase: 'uploading' | 'completing') => void
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
): Promise<CompletedPart> {
  const partNumber = index + 1
  const start = index * session.part_size
  const chunk = file.slice(start, Math.min(start + session.part_size, file.size))
  const maxAttempts = UPLOAD_LIMITS.partAttempts
  let lastError: UploadError | null = null

  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    ensureNotAborted(signal)
    let retryable: UploadError | null = null
    try {
      // Always re-sign on retry: an earlier short-lived URL may have expired.
      let signed: SignedPart
      try {
        signed = await gateway.sign(session.id, partNumber)
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
    if (!Number.isInteger(session.part_size) || session.part_size < 1) {
      throw new UploadError('invalid-response', '服务端返回了无效的分片大小', {
        context: { uploadId: session.id },
      })
    }
    const partCount = Math.ceil(file.size / session.part_size)
    if (!Number.isSafeInteger(partCount) || partCount < 1 || partCount > UPLOAD_LIMITS.maxParts) {
      throw new UploadError('invalid-response', '文件大小超出单次 Multipart 上传限制', {
        context: { uploadId: session.id },
      })
    }

    options.onPhase?.('uploading')
    const concurrency = Math.max(1, Math.min(UPLOAD_LIMITS.maxConcurrency, Math.floor(options.concurrency ?? UPLOAD_LIMITS.maxConcurrency)))
    const completed = new Array<CompletedPart>(partCount)
    let nextIndex = 0
    let finished = 0
    let firstFailure: unknown = null

    await Promise.all(Array.from({ length: Math.min(concurrency, partCount) }, async () => {
      while (!controller.signal.aborted) {
        const index = nextIndex++
        if (index >= partCount) return
        try {
          const part = await uploadPart(gateway, session!, file, index, controller.signal)
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
    completing = true
    options.onPhase?.('completing')
    // The Go control plane can safely replay completion with the SAME ordered parts:
    // this handles the object_completed -> committed recovery path after a lost response.
    // Do not abort once completion may have reached the server.
    for (let attempt = 0; attempt < UPLOAD_LIMITS.completionAttempts; attempt += 1) {
      try {
        await gateway.complete(session.id, completed)
        return
      } catch (completeError) {
        let remote: UploadSession | null = null
        try {
          remote = await gateway.read(session.id)
        } catch {
          remote = null
        }
        if (remote?.status === 'committed') return
        if (remote && ['failed', 'aborted', 'expired'].includes(remote.status)) {
          throw new UploadError('storage-rejected', `上传已终止（服务端状态：${remote.status}）`, {
            context: { uploadId: session.id, attempt: attempt + 1 },
            cause: completeError,
          })
        }
        // A 4xx from the control plane is a deterministic refusal, not an unknown outcome:
        // retrying the identical payload cannot change it. 401/403 falls out here too, so
        // an expired login stops immediately instead of reporting "unknown completion".
        if (isApiRefusal(completeError)) {
          throw new UploadError('non-retryable', `文件提交被拒绝：${(completeError as Error).message}`, {
            context: { uploadId: session.id, attempt: attempt + 1 },
            cause: completeError,
          })
        }
        if (attempt === UPLOAD_LIMITS.completionAttempts - 1) {
          throw new UploadCompletionUnknownError(session.id)
        }
        await new Promise<void>((resolve) => setTimeout(resolve, 200 * (attempt + 1)))
      }
    }
  } catch (error) {
    // The primary error always wins. Cleanup is attempted only when no completion was
    // sent, and any cleanup failure is attached as metadata rather than thrown.
    const primary = error instanceof UploadError ? error : classifyFetchError(error)
    if (session && !completing) {
      const cleanup = await abortQuietly(gateway, session.id)
      if (cleanup) throw new UploadFailure(primary, cleanup)
    }
    throw primary
  } finally {
    options.signal?.removeEventListener('abort', externalAbort)
  }
}

/** User-facing message for any upload rejection, preserving structured detail when present. */
export function uploadErrorMessage(error: unknown): string {
  if (error instanceof UploadError) return error.message
  if (error instanceof Error) return error.message
  return '上传失败'
}
