/**
 * Live end-to-end flow against a real control plane, real PostgreSQL, and real
 * SeaweedFS. Every other spec replaces the API with route fixtures; this one exists
 * to prove the browser-to-storage data plane actually works.
 *
 * Opt-in, because it needs external services:
 *
 *   ASTERIA_E2E_API=http://127.0.0.1:18091 \
 *   ASTERIA_E2E_TOKEN=e2e-live-token-... \
 *   npm run test:e2e
 *
 * `ASTERIA_E2E_API` is also read by vite.config.ts, which proxies /api from the
 * preview server so the console keeps using same-origin requests.
 */
import { expect, test } from '@playwright/test'
import { readFile } from 'node:fs/promises'

const apiBase = process.env.ASTERIA_E2E_API
const token = process.env.ASTERIA_E2E_TOKEN

test.describe('live stack', () => {
  test.skip(!apiBase || !token, 'set ASTERIA_E2E_API and ASTERIA_E2E_TOKEN to run against a real stack')

  test('uploads a two-part file to real S3, lists it, and downloads the exact bytes', async ({ page, context }) => {
    // Eight megabytes plus a remainder guarantees two parts at the default 8 MiB part
    // size, so the multipart path and part ordering are exercised for real.
    const payload = Buffer.alloc(8 * 1024 * 1024 + 137)
    for (let index = 0; index < payload.length; index += 1) payload[index] = index % 251
    const name = `live-${Date.now()}.bin`

    await page.goto('/')
    await page.locator('zw-input input').first().fill(token!)
    await page.locator('.primary-action').click()
    await expect(page.getByRole('heading', { name: '文件空间' })).toBeVisible()

    await page.locator('input[type="file"]').setInputFiles({
      name,
      mimeType: 'application/octet-stream',
      buffer: payload,
    })
    await expect(page.getByText(`已上传「${name}」`)).toBeVisible({ timeout: 120_000 })
    await expect(page.locator('.file-row').filter({ hasText: name })).toBeVisible()

    // The popup stays on about:blank on purpose: the presigned response carries
    // Content-Disposition: attachment, so Chromium starts a download instead of
    // navigating. Assert the download itself, which is the real user-visible outcome.
    const popupPromise = context.waitForEvent('page')
    const row = page.locator('.file-row').filter({ hasText: name })
    await row.getByRole('button', { name: '下载' }).click()
    const popup = await popupPromise
    const download = await Promise.race([
      popup.waitForEvent('download', { timeout: 30_000 }),
      page.waitForEvent('download', { timeout: 30_000 }),
    ])

    const downloaded = await download.path()
    expect(downloaded).toBeTruthy()
    const bytes = await readFile(downloaded!)
    expect(bytes.length).toBe(payload.length)
    expect(bytes.equals(payload)).toBe(true)
    await expect(page.getByText('浏览器拦截了下载窗口')).toHaveCount(0)

    // Clean up through the real API so repeated local runs do not accumulate files.
    const children = await page.request.get(
      `${apiBase}/api/v1/directories/${await rootDirectoryId(page, apiBase!, token!)}/children?limit=200`,
      { headers: { Authorization: `Bearer ${token}` } },
    )
    const listing = (await children.json()) as { data: Array<{ id: string; name: string; revision: number }> }
    const uploaded = listing.data.find((node) => node.name === name)
    expect(uploaded).toBeTruthy()
    if (uploaded) {
      const deleted = await page.request.delete(`${apiBase}/api/v1/nodes/${uploaded.id}`, {
        headers: { Authorization: `Bearer ${token}`, 'If-Match': `"${uploaded.revision}"` },
      })
      expect([204, 404]).toContain(deleted.status())
    }
  })
})

async function rootDirectoryId(page: import('@playwright/test').Page, base: string, bearer: string) {
  const response = await page.request.get(`${base}/api/v1/tenant`, { headers: { Authorization: `Bearer ${bearer}` } })
  const body = (await response.json()) as { data: { root_directory_id: string } }
  return body.data.root_directory_id
}
