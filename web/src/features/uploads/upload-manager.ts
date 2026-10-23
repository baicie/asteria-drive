/**
 * Browser-to-S3 multipart transport. This module never streams file bytes through the API server.
 * A session can be reconciled after an uncertain completion; cross-reload part resume is NOT supported.
 */
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

export class UploadCancelledError extends Error {
  constructor() {
    super('上传已取消')
    this.name = 'UploadCancelledError'
  }
}

export class UploadCompletionUnknownError extends Error {
  readonly uploadId: string

  constructor(uploadId: string) {
    super('文件提交结果暂时无法确认，请刷新目录或联系管理员核对上传状态，勿直接重复上传')
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

function canRetry(status: number) {
  return status === 401 || status === 403 || status === 408 || status === 429 || status >= 500
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
  const maxAttempts = 4

  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    ensureNotAborted(signal)
    try {
      // Always re-sign on retry: an earlier short-lived URL may have expired.
      const signed = await gateway.sign(session.id, partNumber)
      ensureNotAborted(signal)
      if (signed.part_number !== partNumber || signed.method !== 'PUT') {
        throw new Error('签名返回的分片信息不匹配')
      }
      const response = await fetch(signed.url, {
        method: signed.method,
        headers: signed.required_headers ?? {},
        body: chunk,
        signal,
      })
      if (!response.ok) {
        if (!canRetry(response.status)) {
          throw new Error(`HTTP ${response.status}（不可重试）`)
        }
        if (attempt === maxAttempts) throw new Error(`HTTP ${response.status}`)
      } else {
        const etag = response.headers.get('ETag')
        if (!etag) throw new Error('对象存储未返回 ETag')
        return { part_number: partNumber, etag, size: chunk.size }
      }
    } catch (error) {
      if (signal.aborted) throw new UploadCancelledError()
      if (error instanceof Error && (error.message.includes('不可重试') || error.message.includes('不匹配'))) {
        throw new Error(`分片 ${partNumber} 上传失败：${error.message}`)
      }
      if (attempt === maxAttempts) throw new Error(`分片 ${partNumber} 上传重试耗尽`)
    }
    await pause(150 * 2 ** (attempt - 1), signal)
  }
  throw new Error(`分片 ${partNumber} 上传失败`)
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
    session = await gateway.create(file, parentId, idempotencyKey)
    ensureNotAborted(controller.signal)
    if (!Number.isInteger(session.part_size) || session.part_size < 1) {
      throw new Error('服务端返回了无效的分片大小')
    }
    const partCount = Math.ceil(file.size / session.part_size)
    if (!Number.isSafeInteger(partCount) || partCount < 1 || partCount > 10000) {
      throw new Error('文件大小超出单次 Multipart 上传限制')
    }

    options.onPhase?.('uploading')
    const concurrency = Math.max(1, Math.min(3, Math.floor(options.concurrency ?? 3)))
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
    for (let attempt = 0; attempt < 3; attempt += 1) {
      try {
        await gateway.complete(session.id, completed)
        return
      } catch {
        const remote = await gateway.read(session.id).catch(() => null)
        if (remote?.status === 'committed') return
        if (remote && ['failed', 'aborted', 'expired'].includes(remote.status)) {
          throw new Error(`上传已终止（服务端状态：${remote.status}）`)
        }
        if (attempt === 2) throw new UploadCompletionUnknownError(session.id)
        await new Promise<void>((resolve) => setTimeout(resolve, 200 * (attempt + 1)))
      }
    }
  } catch (error) {
    if (session && !completing) {
      // No completion was sent: this is safe to abort after workers have settled.
      try {
        await gateway.abort(session.id)
      } catch {
        throw new Error('上传失败，且清理远端 Multipart 会话未确认；请核对上传状态')
      }
    }
    throw error
  } finally {
    options.signal?.removeEventListener('abort', externalAbort)
  }
}
