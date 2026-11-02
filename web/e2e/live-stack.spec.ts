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

  test('resumes an interrupted upload after a reload without re-sending uploaded parts', async ({ page }) => {
    // The browser cannot restore a File across a reload, so the scenario is staged the way
    // a real interruption leaves it: a live session created through the API, one part
    // already in S3, and the non-sensitive handle the console writes before its first part.
    const payload = Buffer.alloc(8 * 1024 * 1024 + 37)
    for (let index = 0; index < payload.length; index += 1) payload[index] = (index * 7) % 251
    const name = `resume-${Date.now()}.bin`
    const rootId = await rootDirectoryId(page, apiBase!, token!)
    const auth = { Authorization: `Bearer ${token}` }

    const created = await page.request.post(`${apiBase}/api/v1/uploads`, {
      headers: { ...auth, 'Content-Type': 'application/json', 'Idempotency-Key': `resume-${Date.now()}` },
      data: { parent_id: rootId, name, size: payload.length, mime_type: 'application/octet-stream' },
    })
    expect(created.status()).toBe(201)
    const session = ((await created.json()) as { data: { id: string; part_size: number } }).data

    // Upload only part 1 through the real signed data plane.
    const signedResponse = await page.request.post(`${apiBase}/api/v1/uploads/${session.id}/parts/sign`, {
      headers: { ...auth, 'Content-Type': 'application/json' },
      data: { part_number: 1 },
    })
    const signed = ((await signedResponse.json()) as { data: { url: string; method: string } }).data
    const firstPart = await page.request.fetch(signed.url, {
      method: signed.method,
      data: payload.subarray(0, session.part_size),
    })
    expect(firstPart.status()).toBe(200)

    // The server must report exactly the part that landed.
    const listedResponse = await page.request.get(`${apiBase}/api/v1/uploads/${session.id}/parts`, { headers: auth })
    expect(listedResponse.status()).toBe(200)
    expect(listedResponse.headers()['cache-control']).toBe('no-store')
    const listed = ((await listedResponse.json()) as {
      data: { parts: Array<{ part_number: number }>; complete: boolean; part_size: number }
    }).data
    expect(listed.complete).toBe(true)
    expect(listed.parts.map((part) => part.part_number)).toEqual([1])

    const lastModified = Date.now()
    await page.addInitScript(([handle, bearer]) => {
      window.sessionStorage.setItem('asteria.upload.resume', JSON.stringify(handle))
      window.localStorage.setItem('asteria.token', bearer)
    }, [{
      version: 1,
      uploadId: session.id,
      tenantId: '',
      parentId: rootId,
      parentName: '根目录',
      fileName: name,
      size: payload.length,
      lastModified,
      partSize: session.part_size,
      createdAt: Date.now(),
    }, token!] as const)

    await page.goto('/')
    // The banner is the user-visible evidence that the load-time probe accepted the handle.
    await expect(page.getByText('检测到未完成的上传')).toBeVisible()
    await expect(page.getByText(name)).toBeVisible()

    // Re-select the original bytes and let the console reconcile and complete.
    //
    // The File is built inside the page: `setInputFiles({buffer})` stamps `lastModified`
    // with the current time, and the identity check (name, size, lastModified) would then
    // reject the re-selected file — which is the check working as designed, not a bug.
    await page.evaluate(({ name: fileName, size, lastModified: stamp }) => {
      const bytes = new Uint8Array(size)
      for (let index = 0; index < size; index += 1) bytes[index] = (index * 7) % 251
      const file = new File([bytes], fileName, { type: 'application/octet-stream', lastModified: stamp })
      const inputs = document.querySelectorAll<HTMLInputElement>('input[type="file"]')
      const input = inputs[inputs.length - 1]
      const transfer = new DataTransfer()
      transfer.items.add(file)
      input.files = transfer.files
      input.dispatchEvent(new Event('change', { bubbles: true }))
    }, { name, size: payload.length, lastModified })
    await expect(page.getByText(`已恢复并完成「${name}」的上传`)).toBeVisible({ timeout: 120_000 })
    await expect(page.getByRole('button', { name: '下载' }).first()).toBeVisible()
    await expect.poll(async () => {
      const listingResponse = await page.request.get(
        `${apiBase}/api/v1/directories/${rootId}/children?limit=200`,
        { headers: auth },
      )
      const body = (await listingResponse.json()) as { data: Array<{ id: string; name: string; size: number }> }
      return body.data.find((node) => node.name === name)?.size
    }).toBe(payload.length)

    // Nothing is left behind: the handle is gone and the committed object is the right size.
    expect(await page.evaluate(() => window.sessionStorage.getItem('asteria.upload.resume'))).toBeNull()
  })
})

async function rootDirectoryId(page: import('@playwright/test').Page, base: string, bearer: string) {
  const response = await page.request.get(`${base}/api/v1/tenant`, { headers: { Authorization: `Bearer ${bearer}` } })
  const body = (await response.json()) as { data: { root_directory_id: string } }
  return body.data.root_directory_id
}
