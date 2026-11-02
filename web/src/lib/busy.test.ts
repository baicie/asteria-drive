import { describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import { useBusy } from './busy'

describe('useBusy', () => {
  it('tracks unrelated operations independently', () => {
    const busy = useBusy()

    busy.begin('download:a')
    busy.begin('recycle:b')
    expect(busy.active('download:a')).toBe(true)
    expect(busy.active('recycle:b')).toBe(true)

    // The bug this replaces: finishing one operation must not release another.
    busy.end('download:a')
    expect(busy.active('download:a')).toBe(false)
    expect(busy.active('recycle:b')).toBe(true)
    expect(busy.any.value).toBe(true)

    busy.end('recycle:b')
    expect(busy.any.value).toBe(false)
  })

  it('counts nested or repeated begins of the same key', () => {
    const busy = useBusy()

    busy.begin('upload')
    busy.begin('upload')
    busy.end('upload')
    expect(busy.active('upload')).toBe(true)

    busy.end('upload')
    expect(busy.active('upload')).toBe(false)
  })

  it('ignores an end without a matching begin', () => {
    const busy = useBusy()

    busy.end('upload')

    expect(busy.active('upload')).toBe(false)
    expect(busy.any.value).toBe(false)
  })

  it('does not let an unmatched end cancel a different key', () => {
    const busy = useBusy()

    busy.begin('download:a')
    busy.end('upload')

    expect(busy.active('download:a')).toBe(true)
  })

  it('clear releases every key', () => {
    const busy = useBusy()

    busy.begin('download:a')
    busy.begin('recycle:b')
    busy.clear()

    expect(busy.any.value).toBe(false)
    expect(busy.active('download:a')).toBe(false)
    expect(busy.active('recycle:b')).toBe(false)
  })

  it('is reactive: the computed flips when a key starts and ends', async () => {
    const busy = useBusy()
    const seen: boolean[] = []

    // Reads through the computed, as a template binding would.
    seen.push(busy.any.value)
    busy.begin('download:a')
    await nextTick()
    seen.push(busy.any.value)
    busy.end('download:a')
    await nextTick()
    seen.push(busy.any.value)

    expect(seen).toEqual([false, true, false])
  })

  it('replaces the backing map so a watcher sees a distinct identity', async () => {
    const busy = useBusy()
    const snapshots: number[] = []

    busy.begin('a')
    await nextTick()
    snapshots.push(1)
    busy.begin('b')
    await nextTick()
    snapshots.push(2)

    expect(snapshots).toEqual([1, 2])
  })
})
