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
  canMoveUp: boolean
  canMoveDown: boolean
  canMoveTop: boolean
  canMoveBottom: boolean
}

export type QueueMoveAction =
  | { type: 'top' | 'bottom' }
  | { type: 'up' | 'down'; visibleIds?: readonly string[] }
  | { type: 'drop'; targetId: string; position: 'before' | 'after' }

export function applyQueueOrderIds<T extends QueueTask>(tasks: T[], nextActiveOrderIds: string[]): T[] {
  const activeOrdered = orderedQueueTasks(tasks)
  if (activeOrdered.length <= 1) return tasks

  const activeById = new Map(activeOrdered.map(task => [task.id, task]))
  const seen = new Set<string>()
  const reorderedActive: T[] = []

  for (const id of nextActiveOrderIds) {
    const task = activeById.get(id)
    if (task && !seen.has(id)) {
      seen.add(id)
      reorderedActive.push(task)
    }
  }
  for (const task of activeOrdered) {
    if (!seen.has(task.id)) {
      seen.add(task.id)
      reorderedActive.push(task)
    }
  }

  const unchanged = reorderedActive.every((task, idx) => task.id === activeOrdered[idx]?.id)
  if (unchanged) return tasks

  const existingOrders = activeOrdered
    .map(task => task.queueOrder || 0)
    .filter(order => order > 0)
    .sort((a, b) => a - b)

  const orderPool = existingOrders.length === reorderedActive.length
    ? existingOrders
    : reorderedActive.map((_, idx) => idx + 1)

  const nextOrderById = new Map<string, number>()
  reorderedActive.forEach((task, idx) => {
    nextOrderById.set(task.id, orderPool[idx])
  })

  return tasks.map(task => {
    const nextOrder = nextOrderById.get(task.id)
    if (nextOrder === undefined || task.queueOrder === nextOrder) return task
    return { ...task, queueOrder: nextOrder }
  })
}

export function moveQueueTasks<T extends QueueTask>(
  tasks: T[],
  targetIds: Iterable<string>,
  action: QueueMoveAction
): T[] {
  const activeOrdered = orderedQueueTasks(tasks)
  if (activeOrdered.length <= 1) return tasks

  const orderedIds = activeOrdered.map(task => task.id)
  const movingSet = new Set(targetIds)
  const movingIds = orderedIds.filter(id => movingSet.has(id))
  if (movingIds.length === 0) return tasks

  const visibleSet = (action.type === 'up' || action.type === 'down') && action.visibleIds
    ? new Set(action.visibleIds)
    : undefined
  let nextIds = visibleSet ? orderedIds.filter(id => visibleSet.has(id)) : [...orderedIds]

  if (action.type === 'top') {
    nextIds = [...movingIds, ...orderedIds.filter(id => !movingSet.has(id))]
  } else if (action.type === 'bottom') {
    nextIds = [...orderedIds.filter(id => !movingSet.has(id)), ...movingIds]
  } else if (action.type === 'up') {
    for (let i = 1; i < nextIds.length; i++) {
      if (movingSet.has(nextIds[i]) && !movingSet.has(nextIds[i - 1])) {
        const tmp = nextIds[i - 1]
        nextIds[i - 1] = nextIds[i]
        nextIds[i] = tmp
      }
    }
  } else if (action.type === 'down') {
    for (let i = nextIds.length - 2; i >= 0; i--) {
      if (movingSet.has(nextIds[i]) && !movingSet.has(nextIds[i + 1])) {
        const tmp = nextIds[i + 1]
        nextIds[i + 1] = nextIds[i]
        nextIds[i] = tmp
      }
    }
  } else if (action.type === 'drop') {
    if (movingSet.has(action.targetId)) return tasks
    const remaining = orderedIds.filter(id => !movingSet.has(id))
    const targetIdx = remaining.indexOf(action.targetId)
    if (targetIdx === -1) return tasks
    const insertIdx = action.position === 'before' ? targetIdx : targetIdx + 1
    remaining.splice(insertIdx, 0, ...movingIds)
    nextIds = remaining
  }

  if (visibleSet) {
    // Reuse the visible rows' positions and leave hidden rows in their slots.
    let index = 0
    nextIds = orderedIds.map(id => visibleSet.has(id) ? nextIds[index++] : id)
  }
  return applyQueueOrderIds(tasks, nextIds)
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
  selectedIds: ReadonlySet<string>,
  queueTasks: T[] = visibleTasks
): QueueSelectionSummary<T> {
  const selectedTasks = visibleTasks.filter(task => selectedIds.has(task.id))
  const pausableIds = selectedTasks.filter(isPausableQueueTask).map(task => task.id)
  const resumableIds = selectedTasks.filter(isResumableQueueTask).map(task => task.id)
  const deletableIds = selectedTasks.map(task => task.id)
  const selectedCount = selectedTasks.length
  const totalCount = visibleTasks.length

  let canMoveUp = false
  let seenUnselected = false
  for (let i = 0; i < visibleTasks.length; i++) {
    if (selectedIds.has(visibleTasks[i].id)) {
      if (seenUnselected) {
        canMoveUp = true
        break
      }
    } else {
      seenUnselected = true
    }
  }

  let canMoveDown = false
  let seenUnselectedFromEnd = false
  for (let i = visibleTasks.length - 1; i >= 0; i--) {
    if (selectedIds.has(visibleTasks[i].id)) {
      if (seenUnselectedFromEnd) {
        canMoveDown = true
        break
      }
    } else {
      seenUnselectedFromEnd = true
    }
  }

  const visibleSelection = new Set(deletableIds)
  const canMoveTop = queueTasks.slice(0, selectedCount).some(task => !visibleSelection.has(task.id))
  const canMoveBottom = queueTasks.slice(queueTasks.length - selectedCount).some(task => !visibleSelection.has(task.id))

  return {
    selectedTasks,
    pausableIds,
    resumableIds,
    deletableIds,
    selectedCount,
    totalCount,
    allSelected: totalCount > 0 && selectedCount === totalCount,
    someSelected: selectedCount > 0 && selectedCount < totalCount,
    canMoveUp,
    canMoveDown,
    canMoveTop,
    canMoveBottom
  }
}
