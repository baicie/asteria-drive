import { computed, ref, type ComputedRef } from 'vue'

/**
 * Track concurrent UI operations by key.
 *
 * The console used to hold a single `busy` string. Two operations could be in flight —
 * say a download authorization and a recycle delete — and whichever finished first
 * cleared the flag, re-enabling buttons that were still working. That allowed a second
 * click to fire a duplicate authorization request or a duplicate delete. Keying the
 * tracker removes that cross-talk while keeping a cheap "anything in flight" signal for
 * controls that must lock for the duration of any operation.
 */
export interface BusyTracker {
  readonly any: ComputedRef<boolean>
  active(key: string): boolean
  begin(key: string): void
  end(key: string): void
  clear(): void
}

export function useBusy(): BusyTracker {
  // A new Map on every change keeps the ref identity-triggered for Vue and avoids
  // relying on deep Set/Map reactivity.
  const counts = ref(new Map<string, number>())

  function active(key: string) {
    return (counts.value.get(key) ?? 0) > 0
  }

  function begin(key: string) {
    const next = new Map(counts.value)
    next.set(key, (next.get(key) ?? 0) + 1)
    counts.value = next
  }

  function end(key: string) {
    const next = new Map(counts.value)
    const current = next.get(key) ?? 0
    if (current <= 1) {
      next.delete(key)
    } else {
      next.set(key, current - 1)
    }
    counts.value = next
  }

  function clear() {
    counts.value = new Map()
  }

  return { any: computed(() => counts.value.size > 0), active, begin, end, clear }
}
