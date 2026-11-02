import { ref, type Ref } from 'vue'
import { api } from '../../api/client'
import type { Node } from '../../api/types'
import type { BusyTracker } from '../../lib/busy'
import { newIdempotencyKey } from '../../lib/idempotency'
import { UploadError, uploadErrorMessage, uploadMultipart, type UploadGateway } from './upload-manager'
import type { UploadErrorKind } from './upload-errors'

interface UploadTaskOptions {
  token: Ref<string>
  busy: BusyTracker
  getParent: () => Node | null
  currentDirectory: Ref<Node | null>
  directoryGeneration: () => number
  refreshDirectory: () => Promise<unknown>
  notify: (text: string, kind?: 'success' | 'error') => void
  clearNotice: () => void
}

const UPLOAD_KEY = 'upload'

type UploadPhase = 'uploading' | 'completing' | null

export function useUploadTask({
  token,
  busy,
  getParent,
  currentDirectory,
  directoryGeneration,
  refreshDirectory,
  notify,
  clearNotice,
}: UploadTaskOptions) {
  const progress = ref(0)
  const phase = ref<UploadPhase>(null)
  const cancelling = ref(false)
  const abort = ref<AbortController | null>(null)
  const failureKind = ref<UploadErrorKind | null>(null)
  const failureUploadId = ref<string | null>(null)

  async function start(file: File) {
    const parent = getParent()
    if (!parent) return
    if (abort.value) return
    if (file.size < 1) {
      notify('不能上传空文件', 'error')
      return
    }

    busy.begin(UPLOAD_KEY)
    progress.value = 0
    cancelling.value = false
    phase.value = 'uploading'
    failureKind.value = null
    failureUploadId.value = null
    const controller = new AbortController()
    abort.value = controller
    const uploadToken = token.value
    const parentId = parent.id
    const parentName = parent.name
    const uploadDirectoryGeneration = directoryGeneration()
    clearNotice()

    const gateway: UploadGateway = {
      create: (uploadFile, parentIdValue, key) => api('/api/v1/uploads', {
        method: 'POST',
        token: uploadToken,
        headers: { 'Idempotency-Key': key },
        body: JSON.stringify({
          parent_id: parentIdValue,
          name: uploadFile.name,
          size: uploadFile.size,
          mime_type: uploadFile.type || 'application/octet-stream',
        }),
      }),
      sign: (uploadId, partNumber) => api(`/api/v1/uploads/${encodeURIComponent(uploadId)}/parts/sign`, {
        method: 'POST',
        token: uploadToken,
        body: JSON.stringify({ part_number: partNumber }),
      }),
      complete: async (uploadId, parts) => {
        await api(`/api/v1/uploads/${encodeURIComponent(uploadId)}/complete`, {
          method: 'POST',
          token: uploadToken,
          body: JSON.stringify({ parts }),
        })
      },
      read: (uploadId) => api(`/api/v1/uploads/${encodeURIComponent(uploadId)}`, { token: uploadToken }),
      abort: async (uploadId) => {
        await api(`/api/v1/uploads/${encodeURIComponent(uploadId)}`, { method: 'DELETE', token: uploadToken })
      },
    }

    try {
      await uploadMultipart(gateway, file, parentId, newIdempotencyKey(), {
        signal: controller.signal,
        onProgress: (value) => {
          if (abort.value === controller) progress.value = value
        },
        onPhase: (value) => {
          if (abort.value === controller) phase.value = value
        },
      })
      if (abort.value !== controller) return
      const sameDirectory = currentDirectory.value?.id === parentId && directoryGeneration() === uploadDirectoryGeneration
      if (sameDirectory) await refreshDirectory()
      if (abort.value === controller) {
        notify(sameDirectory ? `已上传「${file.name}」` : `已上传「${file.name}」到「${parentName}」`)
      }
    } catch (error) {
      if (abort.value === controller) {
        const uploadError = error instanceof UploadError ? error : null
        failureKind.value = uploadError?.kind ?? null
        failureUploadId.value = uploadError?.context.uploadId ?? null
        notify(uploadErrorMessage(error), 'error')
      }
    } finally {
      if (abort.value === controller) {
        abort.value = null
        phase.value = null
        cancelling.value = false
        busy.end(UPLOAD_KEY)
        progress.value = 0
      }
    }
  }

  function cancel() {
    if (phase.value !== 'uploading' || !abort.value) return
    cancelling.value = true
    abort.value.abort()
  }

  function cancelForLogout() {
    const wasActive = abort.value !== null
    abort.value?.abort()
    abort.value = null
    phase.value = null
    cancelling.value = false
    progress.value = 0
    failureKind.value = null
    failureUploadId.value = null
    // start()'s finally deliberately skips itself once abort.value has been replaced,
    // so the logout path must release the busy key it started. Guard on wasActive: a
    // logout with no upload in flight must not decrement another operation's count.
    if (wasActive) busy.end(UPLOAD_KEY)
  }

  return { progress, phase, cancelling, failureKind, failureUploadId, start, cancel, cancelForLogout }
}
