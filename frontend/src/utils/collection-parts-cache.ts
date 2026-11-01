import { GetCollectionVideoParts } from '../../wailsjs/go/main/App'
import { bilibili } from '../../wailsjs/go/models'

// Reuse successful reads across modal instances, but refresh periodically.
const cache = new Map<string, { expires: number; promise: Promise<bilibili.CollectionVideo> }>()
const lifetime = 5 * 60 * 1000
let inFlight = 0
const waiting: Array<() => void> = []

async function fetchParts(episode: bilibili.EpisodeInfo): Promise<bilibili.CollectionVideo> {
  if (inFlight >= 4) await new Promise<void>(resolve => waiting.push(resolve))
  else inFlight++
  try {
    return await GetCollectionVideoParts(episode)
  } finally {
    const next = waiting.shift()
    if (next) next()
    else inFlight--
  }
}

export function getCollectionVideoParts(episode: bilibili.EpisodeInfo): Promise<bilibili.CollectionVideo> {
  const key = JSON.stringify(episode)
  const existing = cache.get(key)
  if (existing && existing.expires > Date.now()) return existing.promise
  const entry = { expires: Number.POSITIVE_INFINITY, promise: fetchParts(episode) }
  cache.set(key, entry)
  entry.promise.then(() => {
    entry.expires = Date.now() + lifetime
    while (cache.size > 500) {
      const completed = [...cache].find(([, value]) => Number.isFinite(value.expires))
      if (!completed) break
      cache.delete(completed[0])
    }
  }, () => {
    if (cache.get(key) === entry) cache.delete(key)
  })
  return entry.promise
}
