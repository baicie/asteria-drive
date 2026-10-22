import { expect, test } from '@playwright/test'

test('logs in and completes a multipart upload from the console', async ({ page }) => {
  const rootId = '11111111-1111-4111-8111-111111111111'
  const folderId = '22222222-2222-4222-8222-222222222222'
  const fileId = '33333333-3333-4333-8333-333333333333'
  const folderName = 'browser-smoke'
  const children: Array<Record<string, unknown>> = []

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
        parent_id: folderId,
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
})
