/**
 * Structured upload failure taxonomy.
 *
 * Every failure that escapes the upload pipeline carries a `kind` so callers can
 * decide whether retrying, resuming, re-authenticating, or simply informing the
 * user is the correct response. The original cause is always preserved: cleanup
 * failures are attached as `cleanup` metadata and never replace the primary error.
 */

export type UploadErrorKind =
  | 'network'
  | 'signature-expired'
  | 'unauthorized'
  | 'non-retryable'
  | 'cancelled'
  | 'completion-unknown'
  | 'storage-rejected'
  | 'invalid-response'

export interface UploadErrorContext {
  /** One-based multipart part number when the failure is part-scoped. */
  partNumber?: number
  /** Upload session id, present once the session has been created. */
  uploadId?: string
  /** HTTP status observed for this failure, when there was one. */
  status?: number
  /** Attempt number that produced this failure, one-based. */
  attempt?: number
  /** Number of attempts the pipeline was permitted to make. */
  maxAttempts?: number
}

export class UploadError extends Error {
  readonly kind: UploadErrorKind
  readonly retryable: boolean
  readonly context: UploadErrorContext
  readonly cause?: unknown

  constructor(
    kind: UploadErrorKind,
    message: string,
    options: { retryable?: boolean; context?: UploadErrorContext; cause?: unknown } = {},
  ) {
    super(message)
    this.name = 'UploadError'
    this.kind = kind
    this.retryable = options.retryable ?? false
    this.context = options.context ?? {}
    this.cause = options.cause
  }
}

/**
 * Cleanup (abort) failure metadata. The pipeline attaches this to the primary
 * error instead of throwing a second error, which is what used to mask the real
 * cause of an upload failure.
 */
export interface UploadCleanupFailure {
  uploadId: string
  cause: unknown
}

/** The primary error with an optional, non-destructive cleanup annotation. */
export class UploadFailure extends UploadError {
  readonly cleanup?: UploadCleanupFailure

  constructor(error: UploadError, cleanup?: UploadCleanupFailure) {
    super(error.kind, error.message, {
      retryable: error.retryable,
      context: error.context,
      cause: error.cause,
    })
    this.name = 'UploadFailure'
    this.cleanup = cleanup
  }
}

/** True when cancellation is the real reason behind this rejection. */
export function isCancellation(error: unknown): boolean {
  return error instanceof UploadError && error.kind === 'cancelled'
}

/** Map an HTTP status from the S3 PUT or the control plane to an error kind. */
export function classifyStatus(status: number): { kind: UploadErrorKind; retryable: boolean; message: string } {
  if (status === 401 || status === 403) {
    return { kind: 'unauthorized', retryable: false, message: `授权失败（HTTP ${status}），请重新登录后重试` }
  }
  if (status === 408 || status === 429 || status >= 500) {
    return { kind: 'network', retryable: true, message: `服务暂时不可用（HTTP ${status}）` }
  }
  return { kind: 'non-retryable', retryable: false, message: `请求被拒绝（HTTP ${status}），不可重试` }
}

/**
 * S3 answers an expired presigned URL with 403, exactly as it answers a real
 * authorization failure. The error body is the only discriminator, so this is a
 * heuristic on the server's message. It stays conservative in one direction: the
 * retry it triggers uses a *fresh* signature, so mistaking a denial for an expiry
 * costs at most the bounded part attempts, while missing a genuine expiry would
 * fail an upload that could have succeeded.
 */
export function isExpiredSignatureBody(body: string): boolean {
  return /request has expired|expiredtoken|signature expired|expired request/i.test(body)
}

/** Classify a non-2xx part PUT, distinguishing an expired signature from a denial. */
export function classifyPartResponse(status: number, body: string): { kind: UploadErrorKind; retryable: boolean; message: string } {
  if ((status === 400 || status === 401 || status === 403) && isExpiredSignatureBody(body)) {
    return { kind: 'signature-expired', retryable: true, message: '上传签名已过期，正在重新签名' }
  }
  return classifyStatus(status)
}

/**
 * Classify a rejection from the control-plane API client.
 *
 * `api()` throws `ApiError` for every non-2xx control response, including 401 and 403.
 * Treating those as a transport failure would spend all part attempts re-signing a
 * request the server will keep denying. Only a real transport rejection (a bare
 * TypeError, or an abort) is network-class and worth retrying.
 */
export function classifyControlPlaneError(error: unknown): UploadError {
  if (error instanceof UploadError) return error
  if (error instanceof DOMException && error.name === 'AbortError') {
    return new UploadError('cancelled', '上传已取消')
  }
  if (isApiError(error)) {
    const classified = classifyStatus(error.status)
    return new UploadError(classified.kind, classified.message, {
      retryable: classified.retryable,
      context: { status: error.status },
      cause: error,
    })
  }
  return classifyFetchError(error)
}

/**
 * Classify a rejection thrown by fetch itself (as opposed to a non-2xx response).
 * Browsers report a bare TypeError for DNS/TLS/connection failures and for CORS
 * rejections; both are network-class and worth retrying with a fresh signature.
 */
export function classifyFetchError(error: unknown): UploadError {
  if (error instanceof UploadError) return error
  if (error instanceof DOMException && error.name === 'AbortError') {
    return new UploadError('cancelled', '上传已取消')
  }
  return new UploadError('network', '网络请求失败，正在重试', {
    retryable: true,
    cause: error,
  })
}

/**
 * Structural check for `ApiError` (api/client.ts). Imported lazily by shape rather than
 * by type so this module stays independent of the HTTP client and testable in isolation.
 */
function isApiError(error: unknown): error is { status: number; message: string } {
  return typeof error === 'object' && error !== null &&
    (error as { name?: unknown }).name === 'ApiError' &&
    typeof (error as { status?: unknown }).status === 'number'
}
