import { GetCollectionVideoParts } from '../../wailsjs/go/main/App'
import { bilibili } from '../../wailsjs/go/models'

// Reuse successful reads across modal instances, but refresh periodically.
type CacheEntry = { expires: number; promise: Promise<bilibili.CollectionVideo>; group?: bilibili.CollectionVideo }
const cache = new Map<string, CacheEntry>()
const lifetime = 20 * 60 * 1000
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

export function getCachedCollectionVideoParts(episode: bilibili.EpisodeInfo) {
  const entry = cache.get(JSON.stringify(episode))
  return entry && entry.expires > Date.now() ? entry.group : undefined
}

export function getCollectionVideoParts(episode: bilibili.EpisodeInfo): Promise<bilibili.CollectionVideo> {
  const key = JSON.stringify(episode)
  const existing = cache.get(key)
  if (existing && existing.expires > Date.now()) return existing.promise
  const entry: CacheEntry = { expires: Infinity, promise: fetchParts(episode) }
  cache.set(key, entry)
  entry.promise.then(group => {
    entry.group = group
    entry.expires = Date.now() + lifetime
    if (cache.size > 2048) {
      for (const [oldKey, value] of cache) {
        if (value.group) {
          cache.delete(oldKey)
          break
        }
      }
    }
  }, () => {
    if (cache.get(key) === entry) cache.delete(key)
  })
  return entry.promise
}
