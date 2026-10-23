import { ref, type Ref } from 'vue'
import { apiPage } from '../../api/client'
import type { RecycleEntry } from '../../api/types'
import { appendUnique } from '../../lib/collections'

interface RecycleOptions {
  token: Ref<string>
  notify: (text: string, kind?: 'success' | 'error') => void
  clearNotice: () => void
}

export function useRecycleBin({ token, notify, clearNotice }: RecycleOptions) {
  const entries = ref<RecycleEntry[]>([])
  const loading = ref(false)
  const loadingMore = ref(false)
  const cursor = ref<string | null>(null)
  let generation = 0

  async function load() {
    const requestGeneration = ++generation
    loading.value = true
    loadingMore.value = false
    cursor.value = null
    clearNotice()
    try {
      const page = await apiPage<RecycleEntry>('/api/v1/recycle-bin?limit=100', token.value)
      if (requestGeneration !== generation) return
      entries.value = appendUnique([], page.data, (entry) => entry.node.id)
      cursor.value = page.page.next_cursor
    } catch (error) {
      if (requestGeneration === generation) notify(error instanceof Error ? error.message : '无法读取回收站', 'error')
    } finally {
      if (requestGeneration === generation) loading.value = false
    }
  }

  async function loadMore() {
    const nextCursor = cursor.value
    if (!nextCursor || loading.value || loadingMore.value) return
    const requestGeneration = generation
    loadingMore.value = true
    clearNotice()
    try {
      const page = await apiPage<RecycleEntry>(
        `/api/v1/recycle-bin?limit=100&cursor=${encodeURIComponent(nextCursor)}`,
        token.value,
      )
      if (requestGeneration !== generation) return
      if (page.page.next_cursor === nextCursor) throw new Error('服务端返回了重复的分页游标')
      entries.value = appendUnique(entries.value, page.data, (entry) => entry.node.id)
      cursor.value = page.page.next_cursor
    } catch (error) {
      if (requestGeneration === generation) notify(error instanceof Error ? error.message : '加载更多回收项失败', 'error')
    } finally {
      if (requestGeneration === generation) loadingMore.value = false
    }
  }

  function reset() {
    generation += 1
    entries.value = []
    loading.value = false
    loadingMore.value = false
    cursor.value = null
  }

  return { entries, loading, loadingMore, cursor, load, loadMore, reset }
}
