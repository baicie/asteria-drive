import { computed, ref, type Ref } from 'vue'
import { api, apiPage } from '../../api/client'
import type { Breadcrumb, Node } from '../../api/types'
import { appendUnique } from '../../lib/collections'

interface DirectoryOptions {
  token: Ref<string>
  notify: (text: string, kind?: 'success' | 'error') => void
  clearNotice: () => void
}

export function useDirectory({ token, notify, clearNotice }: DirectoryOptions) {
  const currentDirectory = ref<Node | null>(null)
  const entries = ref<Node[]>([])
  const breadcrumbs = ref<Breadcrumb[]>([])
  const loading = ref(false)
  const loadingMore = ref(false)
  const cursor = ref<string | null>(null)
  let generation = 0

  const fileCount = computed(() => entries.value.filter((item) => item.kind === 'file').length)
  const folderCount = computed(() => entries.value.filter((item) => item.kind === 'directory').length)

  async function load(id: string, nextBreadcrumbs: Breadcrumb[]): Promise<boolean> {
    const requestGeneration = ++generation
    loading.value = true
    loadingMore.value = false
    cursor.value = null
    clearNotice()
    try {
      const [directory, children] = await Promise.all([
        api<Node>(`/api/v1/directories/${encodeURIComponent(id)}`, { token: token.value }),
        apiPage<Node>(`/api/v1/directories/${encodeURIComponent(id)}/children?limit=100`, token.value),
      ])
      if (requestGeneration !== generation) return false
      currentDirectory.value = directory
      entries.value = appendUnique([], children.data, (node) => node.id)
      cursor.value = children.page.next_cursor
      breadcrumbs.value = nextBreadcrumbs
      return true
    } catch (error) {
      if (requestGeneration === generation) {
        notify(error instanceof Error ? error.message : '无法读取文件夹', 'error')
      }
      return false
    } finally {
      if (requestGeneration === generation) loading.value = false
    }
  }

  async function loadMore() {
    const directoryId = currentDirectory.value?.id
    const nextCursor = cursor.value
    if (!directoryId || !nextCursor || loading.value || loadingMore.value) return
    const requestGeneration = generation
    loadingMore.value = true
    clearNotice()
    try {
      const page = await apiPage<Node>(
        `/api/v1/directories/${encodeURIComponent(directoryId)}/children?limit=100&cursor=${encodeURIComponent(nextCursor)}`,
        token.value,
      )
      if (requestGeneration !== generation || currentDirectory.value?.id !== directoryId) return
      if (page.page.next_cursor === nextCursor) throw new Error('服务端返回了重复的分页游标')
      entries.value = appendUnique(entries.value, page.data, (node) => node.id)
      cursor.value = page.page.next_cursor
    } catch (error) {
      if (requestGeneration === generation) notify(error instanceof Error ? error.message : '加载更多文件失败', 'error')
    } finally {
      if (requestGeneration === generation) loadingMore.value = false
    }
  }

  async function refresh(): Promise<boolean> {
    const current = currentDirectory.value
    if (!current) return false
    return load(current.id, breadcrumbs.value)
  }

  function generationSnapshot() {
    return generation
  }

  function reset() {
    generation += 1
    currentDirectory.value = null
    entries.value = []
    breadcrumbs.value = []
    cursor.value = null
    loading.value = false
    loadingMore.value = false
  }

  function invalidate() {
    generation += 1
  }

  return {
    currentDirectory,
    entries,
    breadcrumbs,
    loading,
    loadingMore,
    cursor,
    fileCount,
    folderCount,
    load,
    loadMore,
    refresh,
    generationSnapshot,
    invalidate,
    reset,
  }
}
