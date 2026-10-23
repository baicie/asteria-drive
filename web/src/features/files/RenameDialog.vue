<script setup lang="ts">
import { ref, watch } from 'vue'
import type { Node } from '../../api/types'

const props = defineProps<{
  target: Node
  busy: boolean
}>()

const emit = defineEmits<{
  cancel: []
  submit: [name: string]
}>()

const value = ref('')

watch(() => props.target, (target) => {
  value.value = target.name
}, { immediate: true })

function submit() {
  emit('submit', value.value)
}
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('cancel')">
    <form class="modal-card" @submit.prevent="submit">
      <h2>重命名</h2>
      <p class="muted">输入新的名称，最多 255 个字符。</p>
      <input v-model="value" class="modal-input" maxlength="255" aria-label="新名称" />
      <div class="modal-actions">
        <button class="modal-cancel" type="button" @click="emit('cancel')">取消</button>
        <button class="modal-confirm" type="submit" :disabled="busy">保存</button>
      </div>
    </form>
  </div>
</template>
