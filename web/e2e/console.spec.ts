import { expect, test } from '@playwright/test'

test('logs in, uploads, renames, recycles, restores, and moves from the console', async ({ page }) => {
  const rootId = '11111111-1111-4111-8111-111111111111'
  const folderId = '22222222-2222-4222-8222-222222222222'
  const fileId = '33333333-3333-4333-8333-333333333333'
  const folderName = 'browser-smoke'
  const children: Array<Record<string, unknown>> = []
  const folderChildren: Array<Record<string, unknown>> = []
  const recycleEntries: Array<Record<string, unknown>> = []

  await page.route('**/s3/upload/**', async (route) => {
    await route.fulfill({
      status: 200,
      headers: { ETag: '"browser-smoke-etag"' },
      body: '',
    })
  })

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const method = request.method()

    if (method === 'GET' && url.pathname === '/api/v1/tenant') {
      await route.fulfill({
        json: {
          data: {
            display_name: 'Browser Smoke',
            root_directory_id: rootId,
          },
        },
      })
      return
    }

    if (method === 'GET' && url.pathname === `/api/v1/directories/${rootId}`) {
      await route.fulfill({
        json: {
          data: {
            id: rootId,
            kind: 'directory',
            name: '根目录',
            revision: 1,
          },
        },
      })
      return
    }

    if (method === 'GET' && url.pathname === `/api/v1/directories/${rootId}/children`) {
      await route.fulfill({ json: { data: children, page: { next_cursor: null } } })
      return
    }

    if (method === 'GET' && url.pathname === `/api/v1/directories/${folderId}`) {
      await route.fulfill({
        json: {
          data: {
            id: folderId,
            kind: 'directory',
            name: folderName,
            revision: 1,
          },
        },
      })
      return
    }
    if (method === 'GET' && url.pathname === `/api/v1/directories/${folderId}/children`) {
      await route.fulfill({ json: { data: folderChildren, page: { next_cursor: null } } })
      return
    }
    if (method === 'POST' && url.pathname === '/api/v1/directories') {
      expect(request.headers()['authorization']).toBe('Bearer browser-smoke-token')
      const body = request.postDataJSON() as { name: string }
      expect(body.name).toBe(folderName)
      const folder = {
        id: folderId,
        parent_id: rootId,
        kind: 'directory',
        name: folderName,
        revision: 1,
      }
      children.splice(0, children.length, folder)
      await route.fulfill({ status: 201, json: { data: folder } })
      return
    }

    if (method === 'POST' && url.pathname === '/api/v1/uploads') {
      await route.fulfill({
        status: 201,
        json: {
          data: {
            id: '44444444-4444-4444-8444-444444444444',
            status: 'created',
            part_size: 8 * 1024 * 1024,
            expected_size: 12,
          },
        },
      })
      return
    }

    if (method === 'POST' && url.pathname.endsWith('/parts/sign')) {
      await route.fulfill({
        json: {
          data: {
            part_number: 1,
            method: 'PUT',
            url: '/s3/upload/part-1',
            required_headers: {},
          },
        },
      })
      return
    }

    if (method === 'POST' && url.pathname.endsWith('/complete')) {
      const file = {
        id: fileId,
        parent_id: rootId,
        kind: 'file',
        name: 'browser-smoke.txt',
        size: 12,
        mime_type: 'text/plain',
        revision: 1,
      }
      children.push(file)
      await route.fulfill({
        status: 201,
        json: {
          data: {
            upload: { id: '44444444-4444-4444-8444-444444444444', status: 'committed' },
            file,
          },
        },
      })
      return
    }

    if (method === 'PATCH' && url.pathname.startsWith('/api/v1/nodes/')) {
      const id = url.pathname.slice('/api/v1/nodes/'.length)
      const node = children.find((item) => item.id === id) ?? folderChildren.find((item) => item.id === id)
      if (!node) {
        await route.fulfill({ status: 404, json: { error: { message: 'not found' } } })
        return
      }
      const body = request.postDataJSON() as { name?: string; parent_id?: string }
      if (body.name) node.name = body.name
      if (body.parent_id && body.parent_id !== node.parent_id) {
        const rootIndex = children.findIndex((item) => item.id === id)
        if (rootIndex >= 0) children.splice(rootIndex, 1)
        const folderIndex = folderChildren.findIndex((item) => item.id === id)
        if (folderIndex >= 0) folderChildren.splice(folderIndex, 1)
        node.parent_id = body.parent_id
        if (body.parent_id === folderId) folderChildren.push(node)
        else children.push(node)
      }
      node.revision = Number(node.revision) + 1
      await route.fulfill({ json: { data: node } })
      return
    }

    if (method === 'DELETE' && url.pathname.startsWith('/api/v1/nodes/')) {
      const id = url.pathname.slice('/api/v1/nodes/'.length)
      const index = children.findIndex((item) => item.id === id)
      if (index >= 0) {
        const [node] = children.splice(index, 1)
        recycleEntries.push({
          node,
          original_parent_id: String(node.parent_id ?? rootId),
          deleted_at: '2026-10-22T12:00:00Z',
        })
      }
      await route.fulfill({ status: 204, body: '' })
      return
    }

    if (method === 'GET' && url.pathname === '/api/v1/recycle-bin') {
      await route.fulfill({ json: { data: recycleEntries, page: { next_cursor: null } } })
      return
    }

    if (method === 'POST' && url.pathname.startsWith('/api/v1/recycle-bin/') && url.pathname.endsWith('/restore')) {
      const id = url.pathname.slice('/api/v1/recycle-bin/'.length, -'/restore'.length)
      const index = recycleEntries.findIndex((entry) => (entry.node as Record<string, unknown>).id === id)
      if (index < 0) {
        await route.fulfill({ status: 404, json: { error: { message: 'not found' } } })
        return
      }
      const [entry] = recycleEntries.splice(index, 1)
      const node = entry.node as Record<string, unknown>
      children.push(node)
      await route.fulfill({ json: { data: node } })
      return
    }

    if (method === 'DELETE' && url.pathname.startsWith('/api/v1/recycle-bin/')) {
      const id = url.pathname.slice('/api/v1/recycle-bin/'.length)
      const index = recycleEntries.findIndex((entry) => (entry.node as Record<string, unknown>).id === id)
      if (index >= 0) recycleEntries.splice(index, 1)
      await route.fulfill({ status: 204, body: '' })
      return
    }

    await route.fulfill({
      status: 500,
      json: { error: { message: `unexpected browser request: ${method} ${url.pathname}` } },
    })
  })

  await page.goto('/')
  const tokenInput = page.locator('zw-input input').first()
  await expect(tokenInput).toBeVisible()
  await tokenInput.fill('browser-smoke-token')
  await page.locator('.primary-action').click()

  await expect(page.getByRole('heading', { name: '文件空间' })).toBeVisible()
  await expect(page.getByText('Browser Smoke')).toBeVisible()

  const folderInput = page.locator('.new-folder input')
  await expect(folderInput).toBeVisible()
  await expect(folderInput).toHaveJSProperty('tagName', 'INPUT')
  await folderInput.fill(folderName)
  await page.getByRole('button', { name: '新建文件夹' }).click()
  await expect(page.locator('.file-row').getByText(folderName)).toBeVisible()

  await page.locator('input[type="file"]').setInputFiles({
    name: 'browser-smoke.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('browser data'),
  })
  await expect(page.getByText('已上传「browser-smoke.txt」')).toBeVisible()
  await expect(page.locator('.file-row').getByText('browser-smoke.txt')).toBeVisible()

  const fileRow = page.locator('.file-row').filter({ hasText: 'browser-smoke.txt' })
  await fileRow.getByRole('button', { name: '重命名' }).click()
  await expect(page.getByRole('heading', { name: '重命名' })).toBeVisible()
  await page.locator('.modal-input').fill('renamed.txt')
  await page.getByRole('button', { name: '保存' }).click()
  await expect(page.getByText('已重命名为「renamed.txt」')).toBeVisible()
  await expect(page.locator('.file-row').getByText('renamed.txt')).toBeVisible()

  page.once('dialog', (dialog) => dialog.accept())
  await page.locator('.file-row').filter({ hasText: 'renamed.txt' }).getByRole('button', { name: '回收' }).click()
  await expect(page.getByText('已将「renamed.txt」移入回收站')).toBeVisible()

  await page.getByRole('tab', { name: '回收站' }).click()
  await expect(page.getByRole('heading', { name: '回收站' })).toBeVisible()
  await expect(page.locator('.file-row.recycle-row').getByText('renamed.txt')).toBeVisible()
  await page.locator('.file-row.recycle-row').filter({ hasText: 'renamed.txt' }).getByRole('button', { name: '恢复' }).click()
  await expect(page.getByText('已恢复「renamed.txt」')).toBeVisible()
  await expect(page.locator('.file-row.recycle-row')).toHaveCount(0)

  await page.getByRole('tab', { name: '文件' }).click()
  await expect(page.locator('.file-row').getByText('renamed.txt')).toBeVisible()

  await page.locator('.file-row').filter({ hasText: 'renamed.txt' }).getByRole('button', { name: '移动' }).click()
  await expect(page.getByRole('heading', { name: '移动「renamed.txt」' })).toBeVisible()
  await page.locator('.move-item').filter({ hasText: 'browser-smoke' }).click()
  await page.getByRole('button', { name: '移动到这里' }).click()
  await expect(page.getByText('已移动「renamed.txt」')).toBeVisible()
  await expect(page.locator('.file-row').getByText('renamed.txt')).toHaveCount(0)

  await page.locator('.file-row').filter({ hasText: 'browser-smoke' }).getByRole('button', { name: '打开' }).click()
  await expect(page.locator('.file-row').getByText('renamed.txt')).toBeVisible()
})
