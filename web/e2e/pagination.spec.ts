import { expect, test, type Page } from '@playwright/test'

const rootId = '11111111-1111-4111-8111-111111111111'

type StubNode = {
  id: string
  parent_id: string
  kind: 'file' | 'directory'
  name: string
  size?: number
  revision: number
}

function node(id: string, name: string, kind: StubNode['kind'] = 'file'): StubNode {
  return { id, name, kind, parent_id: rootId, revision: 1, ...(kind === 'file' ? { size: 1 } : {}) }
}

async function logIn(page: Page) {
  await page.goto('/')
  await page.locator('zw-input input').first().fill('pagination-test-token')
  await page.locator('.primary-action').click()
  await expect(page.getByRole('heading', { name: '文件空间' })).toBeVisible()
}

test('loads cursor pages without duplicate files and stops at the final page', async ({ page }) => {
  const first = Array.from({ length: 100 }, (_, i) => node(`file-${i}`, `entry-${i}.txt`))
  const last = node('file-100', 'entry-100.txt')
  const requestedCursors: string[] = []

  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/v1/tenant') {
      await route.fulfill({ json: { data: { display_name: 'Pagination Tenant', root_directory_id: rootId } } })
    } else if (url.pathname === `/api/v1/directories/${rootId}`) {
      await route.fulfill({ json: { data: node(rootId, '根目录', 'directory') } })
    } else if (url.pathname === `/api/v1/directories/${rootId}/children`) {
      const cursor = url.searchParams.get('cursor') ?? ''
      requestedCursors.push(cursor)
      if (!cursor) {
        await route.fulfill({ json: { data: first, page: { next_cursor: 'after-100' } } })
      } else if (cursor === 'after-100') {
        await route.fulfill({ json: { data: [first[99], last], page: { next_cursor: null } } })
      } else {
        await route.fulfill({ status: 400, json: { error: { message: 'unexpected cursor' } } })
      }
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected API route' } } })
    }
  })

  await logIn(page)
  await expect(page.locator('.file-row')).toHaveCount(100)
  await page.getByRole('button', { name: '加载更多文件' }).click()
  await expect(page.locator('.file-row')).toHaveCount(101)
  await expect(page.getByText('entry-99.txt')).toHaveCount(1)
  await expect(page.getByText('entry-100.txt')).toHaveCount(1)
  await expect(page.getByRole('button', { name: '加载更多文件' })).toHaveCount(0)
  expect(requestedCursors).toEqual(['', 'after-100'])
})

test('paginates recycle bin and move destinations independently', async ({ page }) => {
  const file = node('file-to-move', 'target-file.txt')
  const folders = Array.from({ length: 201 }, (_, i) => node(`folder-${i}`, `folder-${String(i).padStart(3, '0')}`, 'directory'))
  const recycled = Array.from({ length: 101 }, (_, i) => ({
    node: node(`recycled-${i}`, `deleted-${i}.txt`),
    original_parent_id: rootId,
    deleted_at: '2026-10-22T12:00:00Z',
  }))
  let moved = false

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    if (url.pathname === '/api/v1/tenant') {
      await route.fulfill({ json: { data: { display_name: 'Pagination Tenant', root_directory_id: rootId } } })
    } else if (url.pathname === `/api/v1/directories/${rootId}`) {
      await route.fulfill({ json: { data: node(rootId, '根目录', 'directory') } })
    } else if (url.pathname === `/api/v1/directories/${rootId}/children`) {
      const limit = url.searchParams.get('limit')
      if (limit === '200') {
        const cursor = url.searchParams.get('cursor')
        await route.fulfill({ json: cursor
          ? { data: [folders[199], folders[200]], page: { next_cursor: null } }
          : { data: folders.slice(0, 200), page: { next_cursor: 'move-page-2' } } })
      } else {
        await route.fulfill({ json: { data: moved ? [] : [file], page: { next_cursor: null } } })
      }
    } else if (url.pathname === '/api/v1/recycle-bin') {
      const cursor = url.searchParams.get('cursor')
      await route.fulfill({ json: cursor
        ? { data: [recycled[99], recycled[100]], page: { next_cursor: null } }
        : { data: recycled.slice(0, 100), page: { next_cursor: 'recycle-page-2' } } })
    } else if (url.pathname === `/api/v1/directories/${folders[200].id}/children`) {
      await route.fulfill({ json: { data: [], page: { next_cursor: null } } })
    } else if (request.method() === 'PATCH' && url.pathname === `/api/v1/nodes/${file.id}`) {
      expect(request.postDataJSON()).toEqual({ parent_id: folders[200].id })
      moved = true
      await route.fulfill({ json: { data: { ...file, parent_id: folders[200].id, revision: 2 } } })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected API route' } } })
    }
  })

  await logIn(page)
  await page.getByRole('tab', { name: '回收站' }).click()
  await expect(page.locator('.file-row.recycle-row')).toHaveCount(100)
  await page.getByRole('button', { name: '加载更多回收项' }).click()
  await expect(page.locator('.file-row.recycle-row')).toHaveCount(101)
  await expect(page.getByRole('button', { name: '加载更多回收项' })).toHaveCount(0)

  await page.getByRole('tab', { name: '文件', exact: true }).click()
  await page.locator('.file-row').filter({ hasText: file.name }).getByRole('button', { name: '移动' }).click()
  await expect(page.locator('.move-item')).toHaveCount(200)
  await page.getByRole('button', { name: '加载更多文件夹' }).click()
  await expect(page.locator('.move-item')).toHaveCount(201)
  await page.locator('.move-item').filter({ hasText: folders[200].name }).click()
  await page.getByRole('button', { name: '移动到这里' }).click()
  await expect(page.getByText(`已移动「${file.name}」`)).toBeVisible()
})

test('ignores obsolete directory responses after returning to root', async ({ page }) => {
  const folder = node('folder-slow', 'Slow folder', 'directory')
  const rootFile = node('file-root', 'root-file.txt')
  let releaseSlow!: () => void
  let startedSlow!: () => void
  const slowStarted = new Promise<void>((resolve) => { startedSlow = resolve })
  const slowGate = new Promise<void>((resolve) => { releaseSlow = resolve })

  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/v1/tenant') {
      await route.fulfill({ json: { data: { display_name: 'Pagination Tenant', root_directory_id: rootId } } })
    } else if (url.pathname === `/api/v1/directories/${rootId}`) {
      await route.fulfill({ json: { data: node(rootId, '根目录', 'directory') } })
    } else if (url.pathname === `/api/v1/directories/${rootId}/children`) {
      await route.fulfill({ json: { data: [folder, rootFile], page: { next_cursor: null } } })
    } else if (url.pathname === `/api/v1/directories/${folder.id}`) {
      await route.fulfill({ json: { data: folder } })
    } else if (url.pathname === `/api/v1/directories/${folder.id}/children`) {
      startedSlow()
      await slowGate
      await route.fulfill({ json: { data: [node('stale-file', 'should-not-appear.txt')], page: { next_cursor: null } } })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected API route' } } })
    }
  })

  await logIn(page)
  await page.locator('.file-row').filter({ hasText: folder.name }).getByRole('button', { name: '打开' }).click()
  await slowStarted
  await page.locator('.breadcrumb button').first().click()
  await expect(page.locator('.file-row').getByText(rootFile.name)).toBeVisible()
  releaseSlow()
  await expect(page.locator('.file-row')).toHaveCount(2)
  await expect(page.getByText('should-not-appear.txt')).toHaveCount(0)
})

test('ignores a directory response that arrives after logout', async ({ page }) => {
  const folder = node('folder-logout', 'Logout folder', 'directory')
  let releaseSlow!: () => void
  let startedSlow!: () => void
  const slowStarted = new Promise<void>((resolve) => { startedSlow = resolve })
  const slowGate = new Promise<void>((resolve) => { releaseSlow = resolve })

  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/v1/tenant') {
      await route.fulfill({ json: { data: { display_name: 'Pagination Tenant', root_directory_id: rootId } } })
    } else if (url.pathname === `/api/v1/directories/${rootId}`) {
      await route.fulfill({ json: { data: node(rootId, '根目录', 'directory') } })
    } else if (url.pathname === `/api/v1/directories/${rootId}/children`) {
      await route.fulfill({ json: { data: [folder], page: { next_cursor: null } } })
    } else if (url.pathname === `/api/v1/directories/${folder.id}`) {
      await route.fulfill({ json: { data: folder } })
    } else if (url.pathname === `/api/v1/directories/${folder.id}/children`) {
      startedSlow()
      await slowGate
      await route.fulfill({ json: { data: [node('after-logout', 'after-logout.txt')], page: { next_cursor: null } } })
    } else {
      await route.fulfill({ status: 404, json: { error: { message: 'unexpected API route' } } })
    }
  })

  await logIn(page)
  await page.locator('.file-row').filter({ hasText: folder.name }).getByRole('button', { name: '打开' }).click()
  await slowStarted
  await page.getByRole('button', { name: '退出' }).click()
  await expect(page.getByRole('heading', { name: '连接你的空间' })).toBeVisible()
  releaseSlow()
  await expect(page.getByRole('heading', { name: '连接你的空间' })).toBeVisible()
  await expect(page.getByRole('heading', { name: '文件空间' })).toHaveCount(0)
  await expect(page.getByText('after-logout.txt')).toHaveCount(0)
})

test('does not mark login connected when the root directory fails', async ({ page }) => {
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/v1/tenant') {
      await route.fulfill({ json: { data: { display_name: 'Pagination Tenant', root_directory_id: rootId } } })
    } else {
      await route.fulfill({ status: 403, json: { error: { message: 'Root access denied' } } })
    }
  })
  await page.goto('/')
  await page.locator('zw-input input').first().fill('pagination-test-token')
  await page.locator('.primary-action').click()
  await expect(page.getByText('根目录读取失败，连接未完成')).toBeVisible()
  await expect(page.getByRole('heading', { name: '连接你的空间' })).toBeVisible()
  await expect(page.getByRole('heading', { name: '文件空间' })).toHaveCount(0)
})
