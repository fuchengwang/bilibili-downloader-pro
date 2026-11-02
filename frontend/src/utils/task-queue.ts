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
