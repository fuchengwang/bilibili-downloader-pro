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
