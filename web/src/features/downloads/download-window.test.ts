import { afterEach, describe, expect, it, vi } from 'vitest'
import { DownloadUrlRejectedError, openDownloadWindow, resolveDownloadUrl } from './download-window'

const base = 'https://console.example.test/files'

describe('resolveDownloadUrl', () => {
  it('accepts an absolute https presigned URL', () => {
    const verdict = resolveDownloadUrl(
      'https://s3.example.test/bucket/object?X-Amz-Signature=abc',
      base,
    )
    expect(verdict.ok).toBe(true)
    if (verdict.ok) expect(verdict.url).toBe('https://s3.example.test/bucket/object?X-Amz-Signature=abc')
  })

  it('accepts an absolute http URL for local development storage', () => {
    const verdict = resolveDownloadUrl('http://127.0.0.1:18333/bucket/key', base)
    expect(verdict.ok).toBe(true)
  })

  it('resolves a same-origin relative path against the current origin', () => {
    const verdict = resolveDownloadUrl('/api/v1/files/abc/content', base)
    expect(verdict.ok).toBe(true)
    if (verdict.ok) expect(verdict.url).toBe('https://console.example.test/api/v1/files/abc/content')
  })

  it.each([
    ['javascript:alert(1)'],
    ['JavaScript:alert(1)'],
    ['data:text/html,<script>alert(1)</script>'],
    ['blob:https://console.example.test/1234'],
    ['file:///etc/passwd'],
    ['vbscript:msgbox(1)'],
  ])('refuses the unsafe scheme %s', (url) => {
    const verdict = resolveDownloadUrl(url, base)
    expect(verdict.ok).toBe(false)
    if (!verdict.ok) expect(verdict.reason).toBe('unsupported-scheme')
  })

  it.each([[null], [undefined], [''], ['   '], [42], [{}]])('refuses the non-string/empty value %s', (value) => {
    const verdict = resolveDownloadUrl(value)
    expect(verdict.ok).toBe(false)
    if (!verdict.ok) expect(verdict.reason).toBe('empty')
  })

  it('refuses a URL carrying embedded credentials', () => {
    const verdict = resolveDownloadUrl('https://user:pass@s3.example.test/key', base)
    expect(verdict.ok).toBe(false)
    if (!verdict.ok) expect(verdict.reason).toBe('invalid')
  })

  it('refuses an unparseable URL', () => {
    const verdict = resolveDownloadUrl('http://[::1', base)
    expect(verdict.ok).toBe(false)
    if (!verdict.ok) expect(verdict.reason).toBe('invalid')
  })

  it('does not treat a protocol-relative URL to another host as same-origin', () => {
    const verdict = resolveDownloadUrl('//evil.example.test/key', base)
    expect(verdict.ok).toBe(true)
    if (verdict.ok) expect(new URL(verdict.url).host).toBe('evil.example.test')
    // Scheme is inherited and therefore still https, so this is allowed by policy but
    // visible to the caller; the presigned URL is expected to point at storage hosts.
  })

  it('exposes the rejection reason through DownloadUrlRejectedError', () => {
    const error = new DownloadUrlRejectedError('unsupported-scheme')
    expect(error.reason).toBe('unsupported-scheme')
    expect(error.message).toContain('协议')
  })
})

describe('openDownloadWindow', () => {
  interface FakePopup {
    closed: boolean
    opener: Window | null
    navigated: string[]
    closeCalls: number
    location: { replace: (url: string) => void }
    close: () => void
  }

  /** Behaves like a real browser: a popup is returned, and `noopener` would return null. */
  function installWindow(features: Array<string | undefined>) {
    const popup: FakePopup = {
      closed: false,
      opener: window,
      navigated: [],
      closeCalls: 0,
      location: {
        replace(url: string) {
          popup.navigated.push(url)
        },
      },
      close() {
        popup.closeCalls += 1
        popup.closed = true
      },
    }
    const open = vi.fn((_url?: string, _target?: string, feature?: string) => {
      features.push(feature)
      // A browser that honours `noopener` returns null and still opens a tab.
      if (typeof feature === 'string' && /(^|,)\s*noopener\s*(,|$)/.test(feature)) return null
      return popup as unknown as Window
    })
    vi.stubGlobal('open', open)
    return { popup, open }
  }

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('opens a tab without requesting the noopener feature and severs the opener', () => {
    const features: Array<string | undefined> = []
    const { popup, open } = installWindow(features)

    const target = openDownloadWindow()

    expect(target).not.toBeNull()
    // Requesting `noopener` as a window feature makes real browsers return null, which
    // would make the caller report a blocked popup while leaving a stray blank tab.
    expect(features).toEqual([undefined])
    expect(open.mock.calls[0][0]).toBe('about:blank')
    expect(popup.opener).toBeNull()
  })

  it('reports a blocked popup as null without touching anything', () => {
    vi.stubGlobal('open', vi.fn(() => null))

    expect(openDownloadWindow()).toBeNull()
  })

  it('navigates a live popup and refuses an unsafe URL without navigating it', () => {
    const features: Array<string | undefined> = []
    const { popup } = installWindow(features)
    const target = openDownloadWindow()!

    target.setUrl('https://s3.example.test/key?sig=abc')
    expect(popup.navigated).toEqual(['https://s3.example.test/key?sig=abc'])
    expect(target.closed).toBe(false)

    expect(() => target.setUrl('javascript:alert(1)')).toThrow(DownloadUrlRejectedError)
    // The unsafe URL must never reach popup.location, and the popup is blanked instead.
    expect(popup.navigated).toEqual(['https://s3.example.test/key?sig=abc', 'about:blank'])
  })

  it('does not navigate a popup the user already closed', () => {
    const features: Array<string | undefined> = []
    const { popup } = installWindow(features)
    const target = openDownloadWindow()!

    popup.closed = true
    target.setUrl('https://s3.example.test/key')
    target.close()

    expect(popup.navigated).toEqual([])
    expect(popup.closeCalls).toBe(0)
    expect(target.closed).toBe(true)
  })

  it('reports the popup closed state so the caller can warn instead of silently failing', () => {
    const features: Array<string | undefined> = []
    const { popup } = installWindow(features)
    const target = openDownloadWindow()!

    expect(target.closed).toBe(false)
    popup.closed = true
    expect(target.closed).toBe(true)
  })
})
