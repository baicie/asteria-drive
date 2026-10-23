export function appendUnique<T>(oldItems: T[], newItems: T[], getId: (item: T) => string): T[] {
  const known = new Set(oldItems.map(getId))
  const result = [...oldItems]
  for (const item of newItems) {
    const id = getId(item)
    if (known.has(id)) continue
    known.add(id)
    result.push(item)
  }
  return result
}
