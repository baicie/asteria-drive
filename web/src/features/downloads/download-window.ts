/**
 * Open the download target synchronously from the click handler. A window opened after
 * an awaited authorization request is commonly blocked as an unsolicited popup.
 *
 * The authorization URL is attacker-influenced in the sense that it comes from the API
 * and ultimately from object-storage configuration. It is never trusted blindly: only
 * http(s) absolute URLs are navigated to, and same-origin relative paths are resolved
 * against the current origin. Anything else (javascript:, data:, blob:, file:) is refused.
 */
export interface DownloadWindow {
  readonly closed: boolean
  setUrl(url: string): void
  close(): void
}

export type DownloadUrlVerdict =
  | { ok: true; url: string }
  | { ok: false; reason: 'empty' | 'unsupported-scheme' | 'invalid' }

const ALLOWED_PROTOCOLS = new Set(['http:', 'https:'])

/**
 * Validate a download authorization URL before navigating to it.
 * Returns an absolute URL string when safe, or a refusal reason.
 */
export function resolveDownloadUrl(raw: unknown, base: string = globalThis.location?.href ?? ''): DownloadUrlVerdict {
  if (typeof raw !== 'string' || raw.trim() === '') return { ok: false, reason: 'empty' }
  let parsed: URL
  try {
    parsed = new URL(raw.trim(), base || undefined)
  } catch {
    return { ok: false, reason: 'invalid' }
  }
  if (!ALLOWED_PROTOCOLS.has(parsed.protocol)) {
    return { ok: false, reason: 'unsupported-scheme' }
  }
  // Reject credentials embedded in the URL: they would be sent to the storage host and
  // are never part of a presigned S3 URL.
  if (parsed.username || parsed.password) {
    return { ok: false, reason: 'invalid' }
  }
  return { ok: true, url: parsed.toString() }
}

export function openDownloadWindow(): DownloadWindow | null {
  let popup: Window | null
  try {
    // Do NOT pass the 'noopener' window feature: a browser that honours it returns null,
    // which would make this function report a blocked popup and abandon the tab it just
    // opened. Sever the opener explicitly instead, after keeping the handle.
    popup = window.open('about:blank', '_blank')
  } catch {
    return null
  }
  if (!popup) return null
  try {
    popup.opener = null
  } catch {
    // Some browsers expose opener as read-only; navigation below is still same-tab.
  }
  return {
    get closed() { return popup.closed },
    setUrl(url: string) {
      const target = resolveDownloadUrl(url)
      if (!target.ok) {
        try { popup.location.replace('about:blank') } catch { /* popup already gone */ }
        throw new DownloadUrlRejectedError(target.reason)
      }
      if (!popup.closed) popup.location.replace(target.url)
    },
    close() {
      if (!popup.closed) popup.close()
    },
  }
}

export class DownloadUrlRejectedError extends Error {
  readonly reason: 'empty' | 'unsupported-scheme' | 'invalid'

  constructor(reason: 'empty' | 'unsupported-scheme' | 'invalid') {
    super(reason === 'unsupported-scheme'
      ? '下载授权地址使用了不受支持的协议，已阻止跳转'
      : '下载授权地址无效，已阻止跳转')
    this.name = 'DownloadUrlRejectedError'
    this.reason = reason
  }
}
