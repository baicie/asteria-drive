import { ref, type Ref } from 'vue'
import { api, ApiError } from '../../api/client'
import type { Node } from '../../api/types'
import type { BusyTracker } from '../../lib/busy'
import { newIdempotencyKey } from '../../lib/idempotency'
import {
  UploadError,
  resumeMultipart,
  uploadErrorMessage,
  uploadMultipart,
  webCryptoPartDigest,
  type ResumeOutcome,
  type UploadGateway,
  type UploadSession,
} from './upload-manager'
import type { UploadErrorKind } from './upload-errors'
import {
  RESUME_HANDLE_VERSION,
  classifyProbeStatus,
  clearResumeHandle,
  readResumeHandle,
  writeResumeHandle,
  type ResumeStorage,
  type UploadPartsListing,
  type UploadResumeHandle,
} from './upload-resume'

interface UploadTaskOptions {
  token: Ref<string>
  busy: BusyTracker
  getParent: () => Node | null
  currentDirectory: Ref<Node | null>
  directoryGeneration: () => number
  refreshDirectory: () => Promise<unknown>
  notify: (text: string, kind?: 'success' | 'error') => void
  clearNotice: () => void
  /** The connected tenant, recorded on the handle for diagnosis only. */
  tenantId?: () => string
  /** Defaults to window.sessionStorage; injectable so tests never touch real browser storage. */
  storage?: ResumeStorage
  /** True when the handle's parent directory is showing, so the listing can be refreshed. */
  isParentVisible?: (parentId: string) => boolean
}

const UPLOAD_KEY = 'upload'

type UploadPhase = 'uploading' | 'completing' | null

/**
 * A session handle whose upload can still be continued. `parentName` is display-only and
 * deliberately not trusted for authorization: the resume call always re-authenticates and
 * the server re-checks the tenant.
 */
export interface PendingResume {
  handle: UploadResumeHandle
  status: string
}

function defaultStorage(): ResumeStorage {
  return window.sessionStorage
}

function isApiStatus(error: unknown, status: number): boolean {
  return error instanceof ApiError && error.status === status
}

export function useUploadTask({
  token,
  busy,
  getParent,
  currentDirectory,
  directoryGeneration,
  refreshDirectory,
  notify,
  clearNotice,
  tenantId = () => '',
  storage = defaultStorage(),
  isParentVisible = () => false,
}: UploadTaskOptions) {
  const progress = ref(0)
  const phase = ref<UploadPhase>(null)
  const cancelling = ref(false)
  const abort = ref<AbortController | null>(null)
  const failureKind = ref<UploadErrorKind | null>(null)
  const failureUploadId = ref<string | null>(null)
  const pendingResume = ref<PendingResume | null>(null)
  // A probe is an authenticated read; a second one adds load and can race the first
  // settlement. connect() runs once, but a remount or a doubled effect must not double it.
  let probing = false
  // True between the server creating the session and this upload settling: it tells the
  // pause/cancel branch whether a handle exists on disk at all.
  let resumeHandleWritten = false
  // Set by pause() just before aborting, so start() can tell the transport which
  // interruption this is.
  let pausing = false

  function makeGateway(uploadToken: string): UploadGateway {
    return {
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
      sign: (uploadId, partNumber, checksum) => api(`/api/v1/uploads/${encodeURIComponent(uploadId)}/parts/sign`, {
        method: 'POST',
        token: uploadToken,
        // The digest is what binds this part number to these bytes: the server records it
        // and the resulting signature makes storage reject any other content.
        body: JSON.stringify(checksum ? { part_number: partNumber, checksum } : { part_number: partNumber }),
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
      parts: (uploadId) => api<UploadPartsListing>(`/api/v1/uploads/${encodeURIComponent(uploadId)}/parts`, {
        token: uploadToken,
      }),
    }
  }

  /**
   * Start an upload and keep a session handle for the whole lifetime of the session, so a
   * reload in the middle of a transfer can offer to continue it. The handle is written only
   * after the server has created the session: before that there is nothing to resume, and a
   * handle without a session id would just be stale storage.
   */
  async function start(file: File, parent = getParent()) {
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
    resumeHandleWritten = false

    try {
      await uploadMultipart(makeGateway(uploadToken), file, parentId, newIdempotencyKey(), {
        signal: controller.signal,
        // Pausing keeps the multipart session and its durable parts; cancelling aborts it.
        // Read at failure time: the user may pause while parts are already in flight.
        interruption: () => (pausing ? 'pause' : 'cancel'),
        digest: webCryptoPartDigest,
        onProgress: (value) => {
          if (abort.value === controller) progress.value = value
        },
        onPhase: (value) => {
          if (abort.value === controller) phase.value = value
        },
        onSession: (session: UploadSession) => {
          if (abort.value !== controller) return
          resumeHandleWritten = true
          writeResumeHandle(storage, {
            version: RESUME_HANDLE_VERSION,
            uploadId: session.id,
            tenantId: tenantId(),
            parentId,
            parentName,
            fileName: file.name,
            size: file.size,
            lastModified: file.lastModified,
            partSize: session.part_size,
            createdAt: Date.now(),
          })
        },
      })
      if (abort.value !== controller) return
      clearResumeHandle(storage)
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
        if (uploadError?.kind === 'cancelled') {
          // The handle was written when the session was created, so it is already on disk
          // in both cases. What differs is the server side: a pause left the multipart
          // upload alive, a cancel aborted it, so only a pause may offer a resume.
          if (pausing && resumeHandleWritten) {
            notify(`已暂停「${file.name}」，可刷新后继续上传`)
          } else {
            clearResumeHandle(storage)
            notify(pausing ? `已暂停「${file.name}」` : `已取消「${file.name}」的上传`)
          }
        } else if (uploadError?.kind === 'completion-unknown') {
          // The completion request may have committed. Never drop the handle and never
          // abort: the server status is the only authority on what happened.
          notify(uploadErrorMessage(error), 'error')
        } else {
          clearResumeHandle(storage)
          notify(uploadErrorMessage(error), 'error')
        }
      }
    } finally {
      if (abort.value === controller) {
        abort.value = null
        phase.value = null
        cancelling.value = false
        pausing = false
        busy.end(UPLOAD_KEY)
        progress.value = 0
      }
    }
  }

  /**
   * Offer a resume for a handle left behind by a previous page load. Called once after the
   * tenant is connected. A handle that the server no longer recognises is discarded
   * silently; only a handle the server still considers live is surfaced.
   *
   * The tenant id recorded by the previous session is not compared: the server already
   * scopes every read to the authenticated tenant, and a mismatch there is a 404.
   */
  async function discoverResume() {
    const handle = readResumeHandle(storage)
    if (!handle) {
      clearResumeHandle(storage)
      return
    }
    if (pendingResume.value || abort.value || probing) return
    probing = true
    try {
      const remote = await api<UploadSession>(`/api/v1/uploads/${encodeURIComponent(handle.uploadId)}`, {
        token: token.value,
      })
      const classification = classifyProbeStatus(remote.status)
      if (classification === 'resumable') {
        pendingResume.value = { handle, status: remote.status }
        return
      }
      clearResumeHandle(storage)
      if (classification === 'committed') {
        notify(`「${handle.fileName}」已在此前完成上传`)
        if (isParentVisible(handle.parentId)) await refreshDirectory()
      } else if (classification === 'terminated') {
        notify(`「${handle.fileName}」的待恢复上传已结束（${remote.status}），请重新上传`, 'error')
      }
    } catch (error) {
      // 404: the session is gone. 401/403: the current identity cannot read it. 400: the
      // persisted id is not even a valid session id. All three make the handle useless, so
      // it goes. Anything else (offline, 5xx) is transient and the handle stays for the
      // next load; surfacing a login error here would be noise.
      if ([400, 401, 403, 404].some((status) => isApiStatus(error, status))) {
        clearResumeHandle(storage)
      }
    } finally {
      probing = false
    }
  }

  function dismissResume() {
    pendingResume.value = null
    clearResumeHandle(storage)
  }

  /**
   * Continue the pending session with a re-selected file. The file identity check inside
   * `resumeMultipart` prevents silently splicing a different file into an existing session.
   */
  async function resume(file: File): Promise<ResumeOutcome | null> {
    const pending = pendingResume.value
    if (!pending || abort.value) return null
    busy.begin(UPLOAD_KEY)
    progress.value = 0
    cancelling.value = false
    phase.value = pending.status === 'completing' || pending.status === 'object_completed' ? 'completing' : 'uploading'
    failureKind.value = null
    failureUploadId.value = pending.handle.uploadId
    const controller = new AbortController()
    abort.value = controller
    const uploadToken = token.value
    clearNotice()

    try {
      const outcome = await resumeMultipart(makeGateway(uploadToken), pending.handle, file, {
        signal: controller.signal,
        digest: webCryptoPartDigest,
        onProgress: (value) => {
          if (abort.value === controller) progress.value = value
        },
        onPhase: (value) => {
          if (abort.value === controller) phase.value = value
        },
      })
      if (abort.value !== controller) return null
      pendingResume.value = null
      clearResumeHandle(storage)
      // Refresh first: loadDirectory clears the notice as it starts, so notifying before it
      // would erase the very message that tells the user the resume succeeded.
      if (isParentVisible(pending.handle.parentId)) await refreshDirectory()
      if (abort.value === controller) notify(`已恢复并完成「${pending.handle.fileName}」的上传`)
      return outcome
    } catch (error) {
      if (abort.value === controller) {
        const uploadError = error instanceof UploadError ? error : null
        failureKind.value = uploadError?.kind ?? null
        failureUploadId.value = uploadError?.context.uploadId ?? null
        if (uploadError?.kind === 'storage-rejected' || uploadError?.kind === 'non-retryable') {
          // The session is unusable; do not keep offering a resume that cannot work.
          pendingResume.value = null
          clearResumeHandle(storage)
        }
        notify(uploadErrorMessage(error), 'error')
      }
      return null
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

  /** Stop the local transfer but keep the server session and its durable parts. */
  function pause() {
    if (phase.value !== 'uploading' || !abort.value) return
    pausing = true
    cancelling.value = true
    abort.value.abort()
  }

  /** Stop the transfer and let the server abort the multipart upload. */
  function cancel() {
    if (phase.value !== 'uploading' || !abort.value) return
    pausing = false
    cancelling.value = true
    abort.value.abort()
  }

  function cancelForLogout() {
    const wasActive = abort.value !== null
    pausing = false
    abort.value?.abort()
    abort.value = null
    phase.value = null
    cancelling.value = false
    resumeHandleWritten = false
    progress.value = 0
    failureKind.value = null
    failureUploadId.value = null
    pendingResume.value = null
    // A resume handle belongs to the signed-in identity; it must not survive a logout.
    clearResumeHandle(storage)
    // start()'s finally deliberately skips itself once abort.value has been replaced,
    // so the logout path must release the busy key it started. Guard on wasActive: a
    // logout with no upload in flight must not decrement another operation's count.
    if (wasActive) busy.end(UPLOAD_KEY)
  }

  return {
    progress,
    phase,
    cancelling,
    failureKind,
    failureUploadId,
    pendingResume,
    start,
    resume,
    discoverResume,
    dismissResume,
    pause,
    cancel,
    cancelForLogout,
  }
}
