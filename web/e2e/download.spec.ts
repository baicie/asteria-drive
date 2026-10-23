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
      __downloadPopup: {
        closed: boolean
        opener: Window | null
        location: { replace: (url: string) => void }
        close: () => void
      } | null
      __downloadUrl?: string
    }
    state.__downloadOpenCalls = 0
    state.__downloadPopup = null
    window.open = (() => {
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
      state.__downloadOpenCalls += 1
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
  let popupOpener: Window | null | undefined

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

  releaseAuthorization()
  await expect.poll(() => page.evaluate(() => (window as unknown as { __downloadUrl?: string }).__downloadUrl))
    .toBe('https://signed.example/report.txt')
  popupOpener = await page.evaluate(() => {
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
