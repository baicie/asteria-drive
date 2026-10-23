import { expect, test, type Page, type Route } from '@playwright/test'

const rootId = '11111111-1111-4111-8111-111111111111'
const uploadId = '44444444-4444-4444-8444-444444444444'

async function login(page: Page) {
  await page.goto('/')
  await page.locator('zw-input input').first().fill('upload-reliability-token')
  await page.locator('.primary-action').click()
  await expect(page.getByRole('heading', { name: '文件空间' })).toBeVisible()
}

function serveBase(route: Route, url: URL, files: Record<string, unknown>[]) {
  if (url.pathname === '/api/v1/tenant') {
    return route.fulfill({ json: { data: { display_name: 'Upload Tests', root_directory_id: rootId } } })
  }
  if (url.pathname === `/api/v1/directories/${rootId}`) {
    return route.fulfill({ json: { data: { id: rootId, kind: 'directory', name: '根目录', revision: 1 } } })
  }
  if (url.pathname === `/api/v1/directories/${rootId}/children`) {
    return route.fulfill({ json: { data: files, page: { next_cursor: null } } })
  }
  return null
}

test('retries transient S3 PUT with a fresh signature and completes sorted parts', async ({ page }) => {
  const files: Record<string, unknown>[] = []
  const signCounts = new Map<number, number>()
  const putCounts = new Map<number, number>()
  let completed = false
  let aborted = false

  await page.route('**/s3/upload/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    const part = Number(path.split('/').pop())
    const count = (putCounts.get(part) ?? 0) + 1
    putCounts.set(part, count)
    if (part === 2 && count === 1) {
      await route.fulfill({ status: 503, body: 'retry this upload part' })
      return
    }
    await route.fulfill({ status: 200, headers: { ETag: `"etag-${part}"` } })
  })

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const base = serveBase(route, url, files)
    if (base) { await base; return }
    if (url.pathname === '/api/v1/uploads' && request.method() === 'POST') {
      await route.fulfill({ status: 201, json: { data: { id: uploadId, status: 'created', part_size: 5, expected_size: 10 } } })
    } else if (url.pathname.endsWith('/parts/sign')) {
      const part = (request.postDataJSON() as { part_number: number }).part_number
      signCounts.set(part, (signCounts.get(part) ?? 0) + 1)
      await route.fulfill({ json: { data: { part_number: part, method: 'PUT', url: `/s3/upload/${part}`, required_headers: {} } } })
    } else if (url.pathname.endsWith('/complete')) {
      const parts = (request.postDataJSON() as { parts: Array<{ part_number: number; size: number }> }).parts
      expect(parts.map((part) => part.part_number)).toEqual([1, 2])
      expect(parts.map((part) => part.size)).toEqual([5, 5])
      completed = true
      files.push({ id: 'uploaded-file', parent_id: rootId, kind: 'file', name: 'two-parts.txt', size: 10, revision: 1 })
      await route.fulfill({ status: 201, json: { data: { upload: { status: 'committed' } } } })
    } else if (url.pathname === `/api/v1/uploads/${uploadId}` && request.method() === 'DELETE') {
      aborted = true
      await route.fulfill({ status: 204, body: '' })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected route' } } })
    }
  })

  await login(page)
  await page.locator('input[type="file"]').setInputFiles({
    name: 'two-parts.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('abcdefghij'),
  })
  await expect(page.getByText('已上传「two-parts.txt」')).toBeVisible()
  expect(completed).toBe(true)
  expect(aborted).toBe(false)
  expect(signCounts.get(2)).toBe(2)
  expect(putCounts.get(2)).toBe(2)
})

test('stops on a non-retryable part failure and aborts the session', async ({ page }) => {
  const files: Record<string, unknown>[] = []
  let signCalls = 0
  let putCalls = 0
  let completeCalls = 0
  let abortAuthorization: string | undefined

  await page.route('**/s3/upload/**', async (route) => {
    putCalls += 1
    await route.fulfill({ status: 404, body: 'part does not exist' })
  })

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const base = serveBase(route, url, files)
    if (base) { await base; return }
    if (url.pathname === '/api/v1/uploads' && request.method() === 'POST') {
      await route.fulfill({ status: 201, json: { data: { id: uploadId, status: 'created', part_size: 8, expected_size: 4 } } })
    } else if (url.pathname.endsWith('/parts/sign')) {
      signCalls += 1
      await route.fulfill({ json: { data: { part_number: 1, method: 'PUT', url: '/s3/upload/1' } } })
    } else if (url.pathname.endsWith('/complete')) {
      completeCalls += 1
      await route.fulfill({ status: 201, json: { data: {} } })
    } else if (url.pathname === `/api/v1/uploads/${uploadId}` && request.method() === 'DELETE') {
      abortAuthorization = request.headers()['authorization']
      await route.fulfill({ status: 204, body: '' })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected route' } } })
    }
  })

  await login(page)
  await page.locator('input[type="file"]').setInputFiles({
    name: 'rejected.txt', mimeType: 'text/plain', buffer: Buffer.from('data'),
  })
  await expect(page.getByText('分片 1 上传失败：HTTP 404（不可重试）')).toBeVisible()
  expect(signCalls).toBe(1)
  expect(putCalls).toBe(1)
  expect(completeCalls).toBe(0)
  await expect.poll(() => abortAuthorization).toBe('Bearer upload-reliability-token')
})

test('reconciles lost completion response without aborting an already committed upload', async ({ page }) => {
  const files: Record<string, unknown>[] = []
  let checked = 0
  let aborted = false

  await page.route('**/s3/upload/**', async (route) => {
    await route.fulfill({ status: 200, headers: { ETag: '"committed-etag"' } })
  })

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const base = serveBase(route, url, files)
    if (base) { await base; return }
    if (url.pathname === '/api/v1/uploads' && request.method() === 'POST') {
      await route.fulfill({ status: 201, json: { data: { id: uploadId, status: 'created', part_size: 8, expected_size: 4 } } })
    } else if (url.pathname.endsWith('/parts/sign')) {
      await route.fulfill({ json: { data: { part_number: 1, method: 'PUT', url: '/s3/upload/1' } } })
    } else if (url.pathname.endsWith('/complete')) {
      files.push({ id: 'committed-file', parent_id: rootId, kind: 'file', name: 'committed.txt', size: 4, revision: 1 })
      await route.fulfill({ status: 503, json: { error: { message: 'completion response was lost' } } })
    } else if (url.pathname === `/api/v1/uploads/${uploadId}` && request.method() === 'GET') {
      checked += 1
      await route.fulfill({ json: { data: { id: uploadId, status: 'committed', part_size: 8, expected_size: 4 } } })
    } else if (url.pathname === `/api/v1/uploads/${uploadId}` && request.method() === 'DELETE') {
      aborted = true
      await route.fulfill({ status: 204, body: '' })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected route' } } })
    }
  })

  await login(page)
  await page.locator('input[type="file"]').setInputFiles({
    name: 'committed.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('done'),
  })
  await expect(page.getByText('已上传「committed.txt」')).toBeVisible()
  expect(checked).toBe(1)
  expect(aborted).toBe(false)
})

test('does not abort when the completion outcome remains unknown', async ({ page }) => {
  const files: Record<string, unknown>[] = []
  let completeCalls = 0
  let readCalls = 0
  let aborted = false

  await page.route('**/s3/upload/**', async (route) => {
    await route.fulfill({ status: 200, headers: { ETag: '"unknown-etag"' } })
  })

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const base = serveBase(route, url, files)
    if (base) { await base; return }
    if (url.pathname === '/api/v1/uploads' && request.method() === 'POST') {
      await route.fulfill({ status: 201, json: { data: { id: uploadId, status: 'created', part_size: 8, expected_size: 4 } } })
    } else if (url.pathname.endsWith('/parts/sign')) {
      await route.fulfill({ json: { data: { part_number: 1, method: 'PUT', url: '/s3/upload/1' } } })
    } else if (url.pathname.endsWith('/complete')) {
      completeCalls += 1
      await route.fulfill({ status: 503, json: { error: { message: 'completion unavailable' } } })
    } else if (url.pathname === `/api/v1/uploads/${uploadId}` && request.method() === 'GET') {
      readCalls += 1
      await route.fulfill({ json: { data: { id: uploadId, status: 'uploading', part_size: 8, expected_size: 4 } } })
    } else if (url.pathname === `/api/v1/uploads/${uploadId}` && request.method() === 'DELETE') {
      aborted = true
      await route.fulfill({ status: 204, body: '' })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected route' } } })
    }
  })

  await login(page)
  await page.locator('input[type="file"]').setInputFiles({
    name: 'unknown.txt', mimeType: 'text/plain', buffer: Buffer.from('data'),
  })
  await expect(page.getByText('文件提交结果暂时无法确认')).toBeVisible()
  expect(completeCalls).toBe(3)
  expect(readCalls).toBe(3)
  expect(aborted).toBe(false)
})

test('cancels an active upload and aborts its multipart session', async ({ page }) => {
  const files: Record<string, unknown>[] = []
  let aborted = false
  let completed = false
  let created = false
  await page.route('**/s3/upload/**', async (route) => {
    await route.fulfill({ status: 503, body: 'temporary failure' })
  })
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const base = serveBase(route, url, files)
    if (base) { await base; return }
    if (url.pathname === '/api/v1/uploads' && request.method() === 'POST') {
      created = true
      await route.fulfill({ status: 201, json: { data: { id: uploadId, status: 'created', part_size: 8, expected_size: 12 } } })
    } else if (url.pathname.endsWith('/parts/sign')) {
      const part = (request.postDataJSON() as { part_number: number }).part_number
      await route.fulfill({ json: { data: { part_number: part, method: 'PUT', url: `/s3/upload/${part}` } } })
    } else if (url.pathname.endsWith('/complete')) {
      completed = true
      await route.fulfill({ status: 201, json: { data: {} } })
    } else if (url.pathname === `/api/v1/uploads/${uploadId}` && request.method() === 'DELETE') {
      aborted = true
      await route.fulfill({ status: 204, body: '' })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected route' } } })
    }
  })

  await login(page)
  await page.locator('input[type="file"]').setInputFiles({
    name: 'cancelled.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('twelve-bytes'),
  })
  await expect.poll(() => created).toBe(true)
  await page.getByRole('button', { name: '取消上传' }).click()
  await expect(page.getByText('上传已取消')).toBeVisible()
  expect(aborted).toBe(true)
  expect(completed).toBe(false)
})


test('replays completion after object_completed and confirms final commit', async ({ page }) => {
  const files: Record<string, unknown>[] = []
  let completeCalls = 0
  let readCalls = 0
  let aborted = false

  await page.route('**/s3/upload/**', async (route) => {
    await route.fulfill({ status: 200, headers: { ETag: '"part-1"' } })
  })
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const base = serveBase(route, url, files)
    if (base) { await base; return }
    if (url.pathname === '/api/v1/uploads' && request.method() === 'POST') {
      await route.fulfill({ status: 201, json: { data: { id: uploadId, status: 'created', part_size: 8, expected_size: 4 } } })
    } else if (url.pathname.endsWith('/parts/sign')) {
      await route.fulfill({ json: { data: { part_number: 1, method: 'PUT', url: '/s3/upload/1' } } })
    } else if (url.pathname.endsWith('/complete')) {
      completeCalls += 1
      if (completeCalls === 1) {
        await route.fulfill({ status: 503, json: { error: { message: 'object written, metadata not yet committed' } } })
      } else {
        files.push({ id: 'replayed-file', kind: 'file', name: 'replayed.txt', parent_id: rootId, size: 4, revision: 1 })
        await route.fulfill({ status: 200, json: { data: { upload: { status: 'committed' } } } })
      }
    } else if (url.pathname === `/api/v1/uploads/${uploadId}` && request.method() === 'GET') {
      readCalls += 1
      await route.fulfill({ json: { data: { id: uploadId, status: 'object_completed', part_size: 8, expected_size: 4 } } })
    } else if (url.pathname === `/api/v1/uploads/${uploadId}` && request.method() === 'DELETE') {
      aborted = true
      await route.fulfill({ status: 204, body: '' })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected route' } } })
    }
  })

  await login(page)
  await page.locator('input[type="file"]').setInputFiles({
    name: 'replayed.txt', mimeType: 'text/plain', buffer: Buffer.from('done'),
  })
  await expect(page.getByText('已上传「replayed.txt」')).toBeVisible()
  expect(completeCalls).toBe(2)
  expect(readCalls).toBe(1)
  expect(aborted).toBe(false)
})

test('logout aborts an active upload using its original credential', async ({ page }) => {
  const files: Record<string, unknown>[] = []
  let abortAuthorization: string | undefined
  let completeCalls = 0

  await page.route('**/s3/upload/**', async (route) => {
    await route.fulfill({ status: 503, body: 'temporary failure' })
  })
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const base = serveBase(route, url, files)
    if (base) { await base; return }
    if (url.pathname === '/api/v1/uploads' && request.method() === 'POST') {
      await route.fulfill({ status: 201, json: { data: { id: uploadId, status: 'created', part_size: 8, expected_size: 12 } } })
    } else if (url.pathname.endsWith('/parts/sign')) {
      const part = (request.postDataJSON() as { part_number: number }).part_number
      await route.fulfill({ json: { data: { part_number: part, method: 'PUT', url: `/s3/upload/${part}` } } })
    } else if (url.pathname.endsWith('/complete')) {
      completeCalls += 1
      await route.fulfill({ status: 201, json: { data: {} } })
    } else if (url.pathname === `/api/v1/uploads/${uploadId}` && request.method() === 'DELETE') {
      abortAuthorization = request.headers()['authorization']
      await route.fulfill({ status: 204, body: '' })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected route' } } })
    }
  })

  await login(page)
  await page.locator('input[type="file"]').setInputFiles({
    name: 'logout.txt', mimeType: 'text/plain', buffer: Buffer.from('twelve-bytes'),
  })
  await expect(page.getByRole('button', { name: '取消上传' })).toBeVisible()
  await page.getByRole('button', { name: '退出' }).click()
  await expect(page.getByRole('heading', { name: '连接你的空间' })).toBeVisible()
  await expect.poll(() => abortAuthorization).toBe('Bearer upload-reliability-token')
  expect(completeCalls).toBe(0)
  await expect(page.getByRole('heading', { name: '文件空间' })).toHaveCount(0)
})

test('does not refresh or misreport when the directory changes during upload', async ({ page }) => {
  const folder = {
    id: '55555555-5555-4555-8555-555555555555',
    parent_id: rootId,
    kind: 'directory',
    name: 'Destination',
    revision: 1,
  }
  const files: Record<string, unknown>[] = [folder]
  let releasePut!: () => void
  let markPutStarted!: () => void
  const putStarted = new Promise<void>((resolve) => { markPutStarted = resolve })
  const putGate = new Promise<void>((resolve) => { releasePut = resolve })

  await page.route('**/s3/upload/**', async (route) => {
    markPutStarted()
    await putGate
    await route.fulfill({ status: 200, headers: { ETag: '"navigated-etag"' } })
  })

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const base = serveBase(route, url, files)
    if (base) { await base; return }
    if (url.pathname === `/api/v1/directories/${folder.id}`) {
      await route.fulfill({ json: { data: folder } })
    } else if (url.pathname === `/api/v1/directories/${folder.id}/children`) {
      await route.fulfill({ json: { data: [], page: { next_cursor: null } } })
    } else if (url.pathname === '/api/v1/uploads' && request.method() === 'POST') {
      await route.fulfill({ status: 201, json: { data: { id: uploadId, status: 'created', part_size: 8, expected_size: 4 } } })
    } else if (url.pathname.endsWith('/parts/sign')) {
      await route.fulfill({ json: { data: { part_number: 1, method: 'PUT', url: '/s3/upload/1' } } })
    } else if (url.pathname.endsWith('/complete')) {
      await route.fulfill({ status: 201, json: { data: { upload: { status: 'committed' } } } })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected route' } } })
    }
  })

  await login(page)
  await page.locator('input[type="file"]').setInputFiles({
    name: 'navigated.bin', mimeType: 'application/octet-stream', buffer: Buffer.from('data'),
  })
  await putStarted
  await page.locator('.file-row').filter({ hasText: folder.name }).getByRole('button', { name: '打开' }).click()
  await expect(page.locator('.state-message.empty-state')).toBeVisible()
  releasePut()
  await expect(page.getByText('已上传「navigated.bin」到「根目录」')).toBeVisible()
  await expect(page.locator('.file-row')).toHaveCount(0)
})
