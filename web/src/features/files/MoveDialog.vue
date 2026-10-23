<script setup lang="ts">
import type { Breadcrumb, Node } from '../../api/types'

defineProps<{
  target: Node
  path: Breadcrumb[]
  directories: Node[]
  loading: boolean
  loadingMore: boolean
  error: string
  cursor: string | null
  busy: boolean
}>()

const emit = defineEmits<{
  close: []
  enter: [directory: Node]
  jump: [index: number]
  loadMore: []
  move: []
}>()
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="modal-card">
      <h2>移动「{{ target.name }}」</h2>
      <p class="muted">选择目标文件夹</p>
      <div class="move-path">
        <template v-for="(item, index) in path" :key="item.id">
          <button type="button" @click="emit('jump', index)">{{ item.name }}</button>
          <span v-if="index < path.length - 1">/</span>
        </template>
      </div>
      <div class="move-list">
        <div v-if="loading" class="move-empty">正在读取…</div>
        <div v-else-if="directories.length === 0" class="move-empty">当前文件夹没有子文件夹</div>
        <template v-else>
          <button
            v-for="directory in directories"
            :key="directory.id"
            class="move-item"
            type="button"
            @click="emit('enter', directory)"
          >
            <span>▰ {{ directory.name }}</span><span>进入</span>
          </button>
        </template>
        <div v-if="!loading && cursor" class="page-more">
          <button type="button" :disabled="loadingMore" @click="emit('loadMore')">
            {{ loadingMore ? '加载中…' : '加载更多文件夹' }}
          </button>
        </div>
      </div>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
      <div class="modal-actions">
        <button class="modal-cancel" type="button" @click="emit('close')">取消</button>
        <button class="modal-confirm" type="button" :disabled="busy" @click="emit('move')">移动到这里</button>
      </div>
    </div>
  </div>
</template>
