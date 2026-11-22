export interface QueueTask {
  id: string
  status: string
  queueOrder?: number
  createdAt?: number
}

// Display the same admission order as the scheduler. Keep the source array in
// newest-first history order, and tolerate snapshots from older executables.
export function orderedQueueTasks<T extends QueueTask>(tasks: T[]): T[] {
  return tasks.map((task, index) => ({ task, index }))
    .filter(({ task }) => task.status !== 'completed')
    .sort((a, b) => {
      if (a.task.queueOrder && b.task.queueOrder) return a.task.queueOrder - b.task.queueOrder
      if (a.task.queueOrder || b.task.queueOrder) return a.task.queueOrder ? 1 : -1
      return (a.task.createdAt || 0) - (b.task.createdAt || 0) || b.index - a.index
    })
    .map(({ task }) => task)
}

// Events received during the initial RPC read are newer than its snapshot.
export function mergeTaskSnapshots<T extends QueueTask>(snapshot: T[], updates: T[]): T[] {
  const result = [...snapshot]
  const positions = new Map(result.map((task, index) => [task.id, index]))
  const added: T[] = []
  for (const task of updates) {
    const index = positions.get(task.id)
    if (index !== undefined) result[index] = task
    else { positions.set(task.id, result.length); result.push(task); added.push(task) }
  }
  // New records belong before the older history, independently of queue order.
  if (!added.length) return result
  const ids = new Set(added.map(task => task.id))
  return [...result.filter(task => ids.has(task.id)).reverse(), ...result.filter(task => !ids.has(task.id))]
}

export interface QueueSelectionSummary<T extends QueueTask> {
  selectedTasks: T[]
  pausableIds: string[]
  resumableIds: string[]
  deletableIds: string[]
  selectedCount: number
  totalCount: number
  allSelected: boolean
  someSelected: boolean
}

export function isPausableQueueTask(task: QueueTask): boolean {
  return task.status === 'downloading' || task.status === 'queued'
}

export function isResumableQueueTask(task: QueueTask): boolean {
  return task.status === 'paused' || task.status === 'error' || task.status === 'cancelled'
}

export function pruneSelectedQueueIds<T extends QueueTask>(selectedIds: ReadonlySet<string>, tasks: T[]): Set<string> {
  const activeIds = new Set(tasks.filter(task => task.status !== 'completed').map(task => task.id))
  const next = new Set<string>()
  for (const id of selectedIds) {
    if (activeIds.has(id)) next.add(id)
  }
  return next
}

export function pruneSelectedHistoryIds<T extends QueueTask>(selectedIds: ReadonlySet<string>, tasks: T[]): Set<string> {
  const completedIds = new Set(tasks.filter(task => task.status === 'completed').map(task => task.id))
  const next = new Set<string>()
  for (const id of selectedIds) {
    if (completedIds.has(id)) next.add(id)
  }
  return next
}

export function parseSizeStringToBytes(sizeStr?: string): number {
  if (!sizeStr) return 0
  const leading = sizeStr.split('/')[0]?.trim() || ''
  const match = leading.match(/^(\d+(?:\.\d+)?)\s*(B|KB|MB|GB|TB)\b/i)
  if (!match) return 0
  const value = Number(match[1])
  if (!Number.isFinite(value) || value <= 0) return 0
  const unit = match[2].toUpperCase()
  const multipliers: Record<string, number> = {
    B: 1,
    KB: 1024,
    MB: 1024 ** 2,
    GB: 1024 ** 3,
    TB: 1024 ** 4
  }
  return Math.round(value * (multipliers[unit] || 1))
}

export function getCompletedTaskBytes(task: { totalBytes?: number; downloadedBytes?: number; sizeStr?: string }): number {
  if (task.totalBytes && task.totalBytes > 0) return task.totalBytes
  if (task.downloadedBytes && task.downloadedBytes > 0) return task.downloadedBytes
  return parseSizeStringToBytes(task.sizeStr)
}

export function formatHistoryTotalSize(tasks: Array<{ totalBytes?: number; downloadedBytes?: number; sizeStr?: string }>): string {
  let total = 0
  for (const task of tasks) {
    total += getCompletedTaskBytes(task)
  }
  if (total <= 0) return ''
  const gb = 1024 ** 3
  const mb = 1024 ** 2
  if (total >= gb) {
    return `${(total / gb).toFixed(2)} GB`
  }
  return `${(total / mb).toFixed(1)} MB`
}

export function toggleQueueTaskSelection(
  selectedIds: ReadonlySet<string>,
  visibleIds: string[],
  targetId: string,
  options?: { shiftKey?: boolean; anchorId?: string | null }
): { selected: Set<string>; anchorId: string } {
  const selected = new Set(selectedIds)
  const anchorIndex = options?.anchorId ? visibleIds.indexOf(options.anchorId) : -1
  const targetIndex = visibleIds.indexOf(targetId)
  if (options?.shiftKey && anchorIndex !== -1 && targetIndex !== -1) {
    const start = Math.min(anchorIndex, targetIndex)
    const end = Math.max(anchorIndex, targetIndex)
    for (let i = start; i <= end; i++) selected.add(visibleIds[i])
    return { selected, anchorId: options.anchorId! }
  }
  if (selected.has(targetId)) selected.delete(targetId)
  else selected.add(targetId)
  return { selected, anchorId: targetId }
}

export function summarizeQueueSelection<T extends QueueTask>(
  visibleTasks: T[],
  selectedIds: ReadonlySet<string>
): QueueSelectionSummary<T> {
  const selectedTasks = visibleTasks.filter(task => selectedIds.has(task.id))
  const pausableIds = selectedTasks.filter(isPausableQueueTask).map(task => task.id)
  const resumableIds = selectedTasks.filter(isResumableQueueTask).map(task => task.id)
  const deletableIds = selectedTasks.map(task => task.id)
  const selectedCount = selectedTasks.length
  const totalCount = visibleTasks.length
  return {
    selectedTasks,
    pausableIds,
    resumableIds,
    deletableIds,
    selectedCount,
    totalCount,
    allSelected: totalCount > 0 && selectedCount === totalCount,
    someSelected: selectedCount > 0 && selectedCount < totalCount
  }
}
