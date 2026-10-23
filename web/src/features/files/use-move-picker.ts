import { ref, type Ref } from 'vue'
import { apiPage } from '../../api/client'
import type { Breadcrumb, Node } from '../../api/types'
import { appendUnique } from '../../lib/collections'

export function useMovePicker({ token }: { token: Ref<string> }) {
  const target = ref<Node | null>(null)
  const path = ref<Breadcrumb[]>([])
  const directories = ref<Node[]>([])
  const loading = ref(false)
  const loadingMore = ref(false)
  const error = ref('')
  const cursor = ref<string | null>(null)
  let generation = 0

  async function load(nextPath: Breadcrumb[]) {
    const current = nextPath[nextPath.length - 1]
    if (!current) return
    const requestGeneration = ++generation
    loading.value = true
    loadingMore.value = false
    cursor.value = null
    error.value = ''
    try {
      const page = await apiPage<Node>(
        `/api/v1/directories/${encodeURIComponent(current.id)}/children?limit=200`,
        token.value,
      )
      if (requestGeneration !== generation || !target.value) return
      directories.value = appendUnique([], page.data.filter((item) => item.kind === 'directory'), (node) => node.id)
      cursor.value = page.page.next_cursor
    } catch (loadError) {
      if (requestGeneration !== generation) return
      error.value = loadError instanceof Error ? loadError.message : '无法读取目标文件夹'
      directories.value = []
    } finally {
      if (requestGeneration === generation) loading.value = false
    }
  }

  async function open(item: Node, rootDirectoryId: string) {
    target.value = item
    path.value = [{ id: rootDirectoryId, name: '根目录' }]
    await load(path.value)
  }

  async function loadMore() {
    const current = path.value[path.value.length - 1]
    const nextCursor = cursor.value
    if (!current || !nextCursor || loading.value || loadingMore.value) return
    const requestGeneration = generation
    loadingMore.value = true
    error.value = ''
    try {
      const page = await apiPage<Node>(
        `/api/v1/directories/${encodeURIComponent(current.id)}/children?limit=200&cursor=${encodeURIComponent(nextCursor)}`,
        token.value,
      )
      if (requestGeneration !== generation || !target.value) return
      if (page.page.next_cursor === nextCursor) throw new Error('服务端返回了重复的分页游标')
      directories.value = appendUnique(
        directories.value,
        page.data.filter((item) => item.kind === 'directory'),
        (node) => node.id,
      )
      cursor.value = page.page.next_cursor
    } catch (loadError) {
      if (requestGeneration === generation) {
        error.value = loadError instanceof Error ? loadError.message : '加载更多目标文件夹失败'
      }
    } finally {
      if (requestGeneration === generation) loadingMore.value = false
    }
  }

  async function enter(directory: Node) {
    if (target.value?.id === directory.id) {
      error.value = '不能移动到自身'
      return
    }
    path.value = [...path.value, { id: directory.id, name: directory.name }]
    await load(path.value)
  }

  async function jump(index: number) {
    path.value = path.value.slice(0, index + 1)
    await load(path.value)
  }

  function close() {
    generation += 1
    target.value = null
    path.value = []
    directories.value = []
    cursor.value = null
    loading.value = false
    loadingMore.value = false
    error.value = ''
  }

  return { target, path, directories, loading, loadingMore, error, cursor, open, loadMore, enter, jump, close }
}
