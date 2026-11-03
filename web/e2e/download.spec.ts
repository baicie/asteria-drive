import { expect, test, type Page } from '@playwright/test'

const rootId = '11111111-1111-4111-8111-111111111111'
const fileId = '33333333-3333-4333-8333-333333333333'

async function logIn(page: Page) {
  await page.goto('/')
  await page.locator('zw-input input').first().fill('download-test-token')
  await page.locator('.primary-action').click()
  await expect(page.getByRole('heading', { name: '文件空间' })).toBeVisible()
}

async function installPopupStub(page: Page) {
  await page.addInitScript(() => {
    const state = window as unknown as {
      __downloadOpenCalls: number
      __downloadOpenFeatures: Array<string | undefined>
      __downloadPopup: {
        closed: boolean
        opener: Window | null
        location: { replace: (url: string) => void }
        close: () => void
      } | null
      __downloadUrl?: string
    }
    state.__downloadOpenCalls = 0
    state.__downloadOpenFeatures = []
    state.__downloadPopup = null
    // Faithful stub: honours the real `noopener` semantic that matters here — browsers
    // return null when the feature is requested — so a regression back to
    // window.open(..., 'noopener') fails this suite instead of shipping green.
    window.open = ((_url?: string, _target?: string, features?: string) => {
      state.__downloadOpenCalls += 1
      state.__downloadOpenFeatures.push(features)
      if (typeof features === 'string' && /(^|,)\s*noopener\s*(,|$)/.test(features)) {
        state.__downloadPopup = null
        return null
      }
      const popup = {
        closed: false,
        opener: window,
        location: {
          replace(url: string) {
            state.__downloadUrl = url
          },
        },
        close() {
          popup.closed = true
        },
      }
      state.__downloadPopup = popup
      return popup as unknown as Window
    }) as typeof window.open
  })
}

async function serveBase(page: Page, file: Record<string, unknown>) {
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/v1/tenant') {
      await route.fulfill({ json: { data: { display_name: 'Download Tests', root_directory_id: rootId } } })
    } else if (url.pathname === `/api/v1/directories/${rootId}`) {
      await route.fulfill({ json: { data: { id: rootId, kind: 'directory', name: '根目录', revision: 1 } } })
    } else if (url.pathname === `/api/v1/directories/${rootId}/children`) {
      await route.fulfill({ json: { data: [file], page: { next_cursor: null } } })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected route' } } })
    }
  })
}

test('opens the download window synchronously and then navigates it to the signed URL', async ({ page }) => {
  const file = { id: fileId, parent_id: rootId, kind: 'file', name: 'report.txt', size: 8, revision: 1 }
  let releaseAuthorization!: () => void
  const authorizationGate = new Promise<void>((resolve) => { releaseAuthorization = resolve })
  let authorizationStarted = false

  await installPopupStub(page)
  await serveBase(page, file)
  await page.route(`**/api/v1/files/${fileId}/download-authorizations`, async (route) => {
    authorizationStarted = true
    await authorizationGate
    await route.fulfill({ json: { data: { url: 'https://signed.example/report.txt' } } })
  })

  await logIn(page)
  await page.getByRole('button', { name: '下载' }).click()
  await expect.poll(() => authorizationStarted).toBe(true)
  expect(await page.evaluate(() => (window as unknown as { __downloadOpenCalls: number }).__downloadOpenCalls)).toBe(1)
  expect(await page.evaluate(() => (window as unknown as { __downloadUrl?: string }).__downloadUrl)).toBeUndefined()
  // Regression guard: requesting 'noopener' as a window feature makes real browsers
  // return null, which would break the pre-open-then-navigate strategy entirely.
  expect(await page.evaluate(() => (window as unknown as { __downloadOpenFeatures: Array<string | undefined> }).__downloadOpenFeatures))
    .toEqual([undefined])

  releaseAuthorization()
  await expect.poll(() => page.evaluate(() => (window as unknown as { __downloadUrl?: string }).__downloadUrl))
    .toBe('https://signed.example/report.txt')
  const popupOpener = await page.evaluate(() => {
    const popup = (window as unknown as { __downloadPopup: { opener: Window | null } }).__downloadPopup
    return popup.opener
  })
  expect(popupOpener).toBeNull()
})

test('closes the pre-opened download window when authorization fails', async ({ page }) => {
  const file = { id: fileId, parent_id: rootId, kind: 'file', name: 'denied.txt', size: 8, revision: 1 }
  await installPopupStub(page)
  await serveBase(page, file)
  await page.route(`**/api/v1/files/${fileId}/download-authorizations`, async (route) => {
    await route.fulfill({ status: 403, json: { error: { message: 'download denied' } } })
  })

  await logIn(page)
  await page.getByRole('button', { name: '下载' }).click()
  await expect(page.getByText('download denied')).toBeVisible()
  const closed = await page.evaluate(() => (window as unknown as { __downloadPopup: { closed: boolean } }).__downloadPopup.closed)
  expect(closed).toBe(true)
})

test('reports a blocked popup without requesting a download authorization', async ({ page }) => {
  const file = { id: fileId, parent_id: rootId, kind: 'file', name: 'blocked.txt', size: 8, revision: 1 }
  let authorizationCalls = 0
  await page.addInitScript(() => {
    window.open = (() => null) as typeof window.open
  })
  await serveBase(page, file)
  await page.route(`**/api/v1/files/${fileId}/download-authorizations`, async (route) => {
    authorizationCalls += 1
    await route.fulfill({ json: { data: { url: 'https://signed.example/blocked.txt' } } })
  })

  await logIn(page)
  await page.getByRole('button', { name: '下载' }).click()
  await expect(page.getByText('浏览器拦截了下载窗口，请允许弹窗后重试')).toBeVisible()
  expect(authorizationCalls).toBe(0)
})

test('uses the real window.open and leaves exactly one navigated popup', async ({ context, page }) => {
  const file = { id: fileId, parent_id: rootId, kind: 'file', name: 'real.txt', size: 8, revision: 1 }
  await serveBase(page, file)
  await page.route(`**/api/v1/files/${fileId}/download-authorizations`, async (route) => {
    await route.fulfill({ json: { data: { url: `${new URL(page.url()).origin}/signed/real.txt` } } })
  })
  // The popup is a separate Page; a page-scoped route would not intercept it, and the
  // preview server would answer with the SPA fallback instead of the signed payload.
  await context.route('**/signed/real.txt', async (route) => {
    await route.fulfill({ status: 200, contentType: 'text/plain', body: 'real-body' })
  })

  await logIn(page)
  const popupPromise = context.waitForEvent('page')
  await page.getByRole('button', { name: '下载' }).click()
  const popup = await popupPromise
  await popup.waitForLoadState()

  // The popup must actually navigate to the signed URL, not stay blank and not be closed.
  await expect.poll(() => popup.url()).toContain('/signed/real.txt')
  await expect(popup.locator('body')).toHaveText('real-body')
  await expect(page.getByText('浏览器拦截了下载窗口')).toHaveCount(0)
  await expect(popup.evaluate(() => window.opener)).resolves.toBeNull()
})

test('refuses a javascript: download URL instead of navigating the popup', async ({ page }) => {
  const file = { id: fileId, parent_id: rootId, kind: 'file', name: 'evil.txt', size: 8, revision: 1 }
  await installPopupStub(page)
  await serveBase(page, file)
  await page.route(`**/api/v1/files/${fileId}/download-authorizations`, async (route) => {
    await route.fulfill({ json: { data: { url: "javascript:window.__pwned='yes'" } } })
  })

  await logIn(page)
  await page.getByRole('button', { name: '下载' }).click()

  await expect(page.getByText('下载授权地址使用了不受支持的协议，已阻止跳转')).toBeVisible()
  // The popup is blanked, never handed the unsafe URL, and the payload never runs.
  expect(await page.evaluate(() => (window as unknown as { __downloadUrl?: string }).__downloadUrl)).toBe('about:blank')
  expect(await page.evaluate(() => (window as unknown as { __pwned?: string }).__pwned)).toBeUndefined()
  expect(await page.evaluate(() => (window as unknown as { __downloadPopup: { closed: boolean } }).__downloadPopup.closed))
    .toBe(true)
})
