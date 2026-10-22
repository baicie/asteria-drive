<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

type NodeKind = 'directory' | 'file'

interface Tenant {
  display_name: string
  root_directory_id: string
}

interface Node {
  id: string
  parent_id?: string
  kind: NodeKind
  name: string
  size?: number
  mime_type?: string
  revision: number
}

interface UploadSession {
  id: string
  status: string
  part_size: number
  expected_size: number
}

interface SignedPart {
  part_number: number
  method: string
  url: string
  required_headers?: Record<string, string>
}

interface CompletedPart {
  part_number: number
  etag: string
  size: number
}

interface Breadcrumb {
  id: string
  name: string
}

type Notice = { kind: 'success' | 'error'; text: string } | null

const token = ref(localStorage.getItem('asteria.token') ?? '')
const tenant = ref<Tenant | null>(null)
const currentDirectory = ref<Node | null>(null)
const entries = ref<Node[]>([])
const breadcrumbs = ref<Breadcrumb[]>([])
const folderName = ref('')
const fileInput = ref<HTMLInputElement | null>(null)
const loading = ref(false)
const busy = ref('')
const uploadProgress = ref(0)
const loginError = ref('')
const notice = ref<Notice>(null)

const connected = computed(() => tenant.value !== null)
const fileCount = computed(() => entries.value.filter((item) => item.kind === 'file').length)
const folderCount = computed(() => entries.value.filter((item) => item.kind === 'directory').length)

function onTokenChange(event: Event) {
  const value = (event as CustomEvent<{ value?: string }>).detail?.value
  if (typeof value === 'string') token.value = value
}

function onFolderNameChange(event: Event) {
  const value = (event as CustomEvent<{ value?: string }>).detail?.value
  if (typeof value === 'string') folderName.value = value
}

function setNotice(text: string, kind: 'success' | 'error' = 'success') {
  notice.value = { text, kind }
}

function clearNotice() {
  notice.value = null
}

function errorMessage(body: unknown, status: number) {
  if (body && typeof body === 'object') {
    const value = body as { error?: { message?: string }; message?: string }
    if (value.error?.message) return value.error.message
    if (value.message) return value.message
  }
  return `请求失败（HTTP ${status}）`
}

async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (token.value) headers.set('Authorization', `Bearer ${token.value}`)
  if (init.body && !(init.body instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  const response = await fetch(path, { ...init, headers })
  const body = response.status === 204 ? null : await response.json().catch(() => null)
  if (!response.ok) throw new Error(errorMessage(body, response.status))
  return ((body as { data?: T } | null)?.data ?? body) as T
}

function newIdempotencyKey() {
  return globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`
}

async function loadDirectory(id: string, nextBreadcrumbs: Breadcrumb[]) {
  loading.value = true
  clearNotice()
  try {
    const [directory, children] = await Promise.all([
      api<Node>(`/api/v1/directories/${encodeURIComponent(id)}`),
      api<Node[]>(`/api/v1/directories/${encodeURIComponent(id)}/children?limit=100`),
    ])
    currentDirectory.value = directory
    entries.value = children ?? []
    breadcrumbs.value = nextBreadcrumbs
  } catch (error) {
    setNotice(error instanceof Error ? error.message : '无法读取文件夹', 'error')
  } finally {
    loading.value = false
  }
}

async function connect() {
  token.value = token.value.trim()
  loginError.value = ''
  clearNotice()
  if (!token.value) {
    loginError.value = '请输入本地开发 Token'
    return
  }

  loading.value = true
  try {
    const result = await api<Tenant>('/api/v1/tenant')
    tenant.value = result
    localStorage.setItem('asteria.token', token.value)
    await loadDirectory(result.root_directory_id, [{ id: result.root_directory_id, name: '根目录' }])
  } catch (error) {
    tenant.value = null
    loginError.value = error instanceof Error ? error.message : '连接失败'
  } finally {
    loading.value = false
  }
}

function logout() {
  token.value = ''
  tenant.value = null
  currentDirectory.value = null
  entries.value = []
  breadcrumbs.value = []
  localStorage.removeItem('asteria.token')
  clearNotice()
}

async function openEntry(item: Node) {
  if (item.kind !== 'directory') return
  await loadDirectory(item.id, [...breadcrumbs.value, { id: item.id, name: item.name }])
}

async function openBreadcrumb(item: Breadcrumb, index: number) {
  await loadDirectory(item.id, breadcrumbs.value.slice(0, index + 1))
}

async function refresh() {
  const current = currentDirectory.value
  if (!current) return
  await loadDirectory(current.id, breadcrumbs.value)
}

async function createFolder() {
  const name = folderName.value.trim()
  const parent = currentDirectory.value
  if (!name || !parent) return
  busy.value = 'folder'
  clearNotice()
  try {
    await api<Node>('/api/v1/directories', {
      method: 'POST',
      headers: { 'Idempotency-Key': newIdempotencyKey() },
      body: JSON.stringify({ parent_id: parent.id, name }),
    })
    folderName.value = ''
    await refresh()
    setNotice(`已创建文件夹「${name}」`)
  } catch (error) {
    setNotice(error instanceof Error ? error.message : '创建文件夹失败', 'error')
  } finally {
    busy.value = ''
  }
}

function chooseFile() {
  fileInput.value?.click()
}

async function uploadFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  const parent = currentDirectory.value
  input.value = ''
  if (!file || !parent) return
  if (file.size < 1) {
    setNotice('不能上传空文件', 'error')
    return
  }

  busy.value = 'upload'
  uploadProgress.value = 0
  clearNotice()
  let uploadId = ''
  try {
    const upload = await api<UploadSession>('/api/v1/uploads', {
      method: 'POST',
      headers: { 'Idempotency-Key': newIdempotencyKey() },
      body: JSON.stringify({
        parent_id: parent.id,
        name: file.name,
        size: file.size,
        mime_type: file.type || 'application/octet-stream',
      }),
    })
    uploadId = upload.id
    if (!Number.isInteger(upload.part_size) || upload.part_size < 1) {
      throw new Error('服务端返回了无效的分片大小')
    }

    const partCount = Math.ceil(file.size / upload.part_size)
    if (partCount > 10000) throw new Error('文件过大，超过单次上传的 10000 个分片上限')
    const completedParts: CompletedPart[] = []
    for (let index = 0; index < partCount; index += 1) {
      const partNumber = index + 1
      const start = index * upload.part_size
      const chunk = file.slice(start, Math.min(start + upload.part_size, file.size))
      const signed = await api<SignedPart>(`/api/v1/uploads/${upload.id}/parts/sign`, {
        method: 'POST',
        body: JSON.stringify({ part_number: partNumber }),
      })
      const objectResponse = await fetch(signed.url, {
        method: signed.method,
        headers: signed.required_headers ?? {},
        body: chunk,
      })
      if (!objectResponse.ok) throw new Error(`对象存储上传失败（HTTP ${objectResponse.status}）`)
      const etag = objectResponse.headers.get('ETag') ?? objectResponse.headers.get('etag') ?? ''
      if (!etag) throw new Error(`分片 ${partNumber} 未返回 ETag`)
      completedParts.push({ part_number: signed.part_number, etag, size: chunk.size })
      uploadProgress.value = Math.round((partNumber / partCount) * 100)
    }
    await api(`/api/v1/uploads/${upload.id}/complete`, {
      method: 'POST',
      body: JSON.stringify({ parts: completedParts }),
    })
    await refresh()
    setNotice(`已上传「${file.name}」`)
  } catch (error) {
    if (uploadId) {
      await api(`/api/v1/uploads/${uploadId}`, { method: 'DELETE' }).catch(() => undefined)
    }
    setNotice(error instanceof Error ? error.message : '上传失败', 'error')
  } finally {
    busy.value = ''
    uploadProgress.value = 0
  }
}

async function downloadFile(item: Node) {
  busy.value = `download:${item.id}`
  clearNotice()
  try {
    const authorization = await api<{ url: string }>('/api/v1/files/' + encodeURIComponent(item.id) + '/download-authorizations', {
      method: 'POST',
      body: '{}',
    })
    window.open(authorization.url, '_blank', 'noopener,noreferrer')
  } catch (error) {
    setNotice(error instanceof Error ? error.message : '下载失败', 'error')
  } finally {
    busy.value = ''
  }
}

function formatSize(size = 0) {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MB`
  return `${(size / 1024 / 1024 / 1024).toFixed(1)} GB`
}

function formatType(item: Node) {
  if (item.kind === 'directory') return '文件夹'
  return item.mime_type || '文件'
}

onMounted(() => {
  if (token.value) connect()
})
</script>

<template>
  <div class="app-shell">
    <header class="topbar">
      <a class="brand" href="/" aria-label="Asteria Drive 首页">
        <span class="brand-mark">A</span>
        <span>
          <strong>Asteria Drive</strong>
          <small>Personal file space</small>
        </span>
      </a>
      <div v-if="connected" class="account-area">
        <span class="connection-dot"></span>
        <span class="tenant-label">{{ tenant?.display_name }}</span>
        <button class="logout-button" type="button" @click="logout">退出</button>
      </div>
    </header>

    <main class="page-content">
      <section v-if="!connected" class="welcome-layout">
        <div class="intro-panel">
          <span class="eyebrow">LOCAL WORKSPACE</span>
          <h1>你的文件，<br /><em>井然有序。</em></h1>
          <p>连接本地 Asteria Drive 租户，安全浏览、上传和管理文件。</p>
          <div class="feature-list">
            <span><i>✓</i> 租户隔离</span>
            <span><i>✓</i> 直传对象存储</span>
            <span><i>✓</i> 可恢复的文件管理</span>
          </div>
        </div>
        <div class="login-card">
          <div class="card-kicker">欢迎回来</div>
          <h2>连接你的空间</h2>
          <p class="muted">使用本地开发 Token 登录控制台</p>
          <label class="field-label" for="token-input">API Token <span>必填</span></label>
          <div class="token-input-shell">
            <span class="token-prefix" aria-hidden="true">⌕</span>
            <zw-input
              id="token-input"
              :value="token"
              type="password"
              autocomplete="off"
              placeholder="请在这里输入 Token"
              @value-change="onTokenChange"
            ></zw-input>
          </div>
          <p v-if="loginError" class="form-error" role="alert">{{ loginError }}</p>
          <zw-button class="primary-action" variant="primary" :disabled="loading" @click="connect">
            {{ loading ? '连接中…' : '进入文件空间' }}
            <span aria-hidden="true">→</span>
          </zw-button>
          <p class="dev-hint">本地默认 Token：<code>local-development-token-change-me-000000000000</code></p>
        </div>
      </section>

      <section v-else class="drive-layout">
        <div class="drive-heading">
          <div>
            <span class="eyebrow">MY FILES</span>
            <h1>文件空间</h1>
          </div>
          <div class="summary-pills">
            <span><b>{{ folderCount }}</b> 个文件夹</span>
            <span><b>{{ fileCount }}</b> 个文件</span>
          </div>
        </div>

        <div class="toolbar">
          <div class="breadcrumb" aria-label="当前位置">
            <template v-for="(item, index) in breadcrumbs" :key="item.id">
              <button type="button" :class="{ current: index === breadcrumbs.length - 1 }" @click="openBreadcrumb(item, index)">
                <span v-if="index === 0" class="home-icon">⌂</span>{{ item.name }}
              </button>
              <span v-if="index < breadcrumbs.length - 1" class="crumb-separator">/</span>
            </template>
          </div>
          <div class="toolbar-actions">
            <form class="new-folder" @submit.prevent="createFolder">
              <zw-input :value="folderName" size="sm" maxlength="255" placeholder="新文件夹名称" @value-change="onFolderNameChange"></zw-input>
              <zw-button variant="secondary" size="sm" :disabled="busy !== ''" type="submit">新建文件夹</zw-button>
            </form>
            <input ref="fileInput" type="file" hidden @change="uploadFile" />
            <zw-button variant="primary" :disabled="busy !== ''" @click="chooseFile">
              <span class="upload-icon">↑</span>
              {{ busy === 'upload' ? `上传中 ${uploadProgress}%` : '上传文件' }}
            </zw-button>
          </div>
        </div>

        <div v-if="notice" class="notice" :class="notice.kind" role="status">{{ notice.text }}</div>

        <div class="file-card">
          <div class="file-card-header">
            <span>名称</span><span>类型</span><span>大小</span><span>操作</span>
          </div>
          <div v-if="loading" class="state-message"><span class="spinner"></span>正在读取文件空间…</div>
          <div v-else-if="entries.length === 0" class="state-message empty-state">
            <span class="empty-icon">✦</span>
            <strong>这里还没有文件</strong>
            <span>新建文件夹或上传一个文件，开始使用你的空间。</span>
          </div>
          <div v-for="item in entries" v-else :key="item.id" class="file-row">
            <button v-if="item.kind === 'directory'" class="file-name" type="button" @click="openEntry(item)">
              <span class="item-icon folder-icon">▰</span><span>{{ item.name }}</span>
            </button>
            <div v-else class="file-name"><span class="item-icon file-icon">▤</span><span>{{ item.name }}</span></div>
            <span class="file-kind">{{ formatType(item) }}</span>
            <span class="file-size">{{ item.kind === 'directory' ? '—' : formatSize(item.size) }}</span>
            <div class="row-action">
              <zw-button v-if="item.kind === 'directory'" variant="secondary" size="sm" @click="openEntry(item)">打开</zw-button>
              <zw-button v-else variant="secondary" size="sm" :disabled="busy === `download:${item.id}`" @click="downloadFile(item)">
                {{ busy === `download:${item.id}` ? '准备中…' : '下载' }}
              </zw-button>
            </div>
          </div>
        </div>
        <div class="drive-footer"><span>由 Zeus UI + Vite 驱动</span><button type="button" @click="refresh">↻ 刷新</button></div>
      </section>
    </main>
  </div>
</template>
