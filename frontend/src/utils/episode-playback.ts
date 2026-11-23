import { computed, onScopeDispose, shallowReactive, watch } from 'vue'
import { GetPlaybackInfo } from '../../wailsjs/go/main/App'
import type { bilibili } from '../../wailsjs/go/models'
import { isPermissionError, nearEpisode } from './episode-selection'

export type EpisodeAccess = {
  state: 'checking' | 'available' | 'blocked' | 'unknown' | 'partial' | 'unchecked'
  reason?: string
}

const checking: EpisodeAccess = { state: 'checking', reason: '正在检查观看权限' }
const unknown: EpisodeAccess = { state: 'unknown', reason: '暂时无法确认观看权限，可以尝试下载' }
const noVideo: EpisodeAccess = { state: 'blocked', reason: 'B 站未提供这一集的视频信息' }
const unchecked: EpisodeAccess = { state: 'unchecked', reason: '下载时检查观看权限' }

type PlaybackResult = { access: EpisodeAccess; qualities?: bilibili.QualityOption[] }
type PlaybackCacheEntry = { expires: number; result?: PlaybackResult; pending?: Promise<PlaybackResult> }
const playbackCache = new Map<string, PlaybackCacheEntry>()

function accountKey(user?: bilibili.UserInfo | null) {
  if (!user) return 'unknown'
  return user.isLogin ? `${user.mid}:${user.vipStatus}:${user.vipType}` : 'guest'
}

export function clearEpisodePlaybackCache() {
  playbackCache.clear()
}

function playbackKey(ep: bilibili.EpisodeInfo, detail: bilibili.VideoDetail, user?: bilibili.UserInfo | null) {
  return JSON.stringify([accountKey(user), detail.type, ep.bvid || detail.bvid, ep.aid || detail.aid, ep.cid, ep.epid || 0])
}

function cachedPlayback(key: string) {
  const entry = playbackCache.get(key)
  if (entry && entry.expires > Date.now()) return entry
  playbackCache.delete(key)
}

async function readPlayback(ep: bilibili.EpisodeInfo, video: bilibili.VideoDetail): Promise<PlaybackResult> {
  try {
    const info = await GetPlaybackInfo(ep.bvid || video.bvid, ep.aid || video.aid, ep.cid, ep.epid || 0,
      video.type === 'bangumi', video.type === 'cheese')
    if (isPermissionError(info?.error?.kind)) return { access: { state: 'blocked', reason: info.error?.message } }
    if (!info?.error && info?.qualities?.some(quality => quality.isAvailable)) {
      return { access: { state: 'available' }, qualities: info.qualities }
    }
    return { access: { state: 'unknown', reason: info?.error?.message || unknown.reason }, qualities: info?.qualities }
  } catch {
    return { access: unknown }
  }
}

function getPlayback(ep: bilibili.EpisodeInfo, detail: bilibili.VideoDetail, user?: bilibili.UserInfo | null) {
  const key = playbackKey(ep, detail, user)
  const existing = cachedPlayback(key)
  if (existing?.result) return Promise.resolve(existing.result)
  if (existing?.pending) return existing.pending
  const entry: PlaybackCacheEntry = { expires: Infinity }
  playbackCache.set(key, entry)
  entry.pending = readPlayback(ep, detail).then(result => {
    entry.result = result
    entry.pending = undefined
    entry.expires = Date.now() + (result.access.state === 'unknown' ? 15_000 : 5 * 60_000)
    if (playbackCache.size > 2048) {
      for (const [oldKey, value] of playbackCache) {
        if (!value.pending) {
          playbackCache.delete(oldKey)
          break
        }
      }
    }
    return result
  })
  return entry.pending
}

export function useEpisodePlayback(
  detail: () => bilibili.VideoDetail,
  account: () => bilibili.UserInfo | null | undefined,
  episodes: () => bilibili.EpisodeInfo[],
  shouldCheck?: (episode: bilibili.EpisodeInfo) => boolean,
) {
  const results = shallowReactive(new Map<number, PlaybackResult>())
  const reference = computed(() => detail().episodes[Math.max(0, (detail().defaultPage || 1) - 1)] || detail().episodes[0])
  const referenceQualities = computed(() => results.get(reference.value?.cid)?.qualities || [])
  const toCheck = computed(() => shouldCheck ? episodes().filter(shouldCheck) : episodes())
  let generation = 0
  let disposed = false
  let running = 0
  let queue: bilibili.EpisodeInfo[] = []

  function accessFor(cid: number): EpisodeAccess {
    if (cid <= 0) return noVideo
    return results.get(cid)?.access || (shouldCheck ? unchecked : checking)
  }

  function canSelect(cid: number) {
    const state = accessFor(cid).state
    return state === 'available' || state === 'unknown' || state === 'unchecked'
  }

  async function check(ep: bilibili.EpisodeInfo, request: number) {
    try {
      const result = await getPlayback(ep, detail(), account())
      if (!disposed && request === generation) results.set(ep.cid, result)
    } finally {
      running--
      pump()
    }
  }

  function pump() {
    while (!disposed && running < 3 && queue.length) {
      const ep = queue.shift()!
      running++
      void check(ep, generation)
    }
  }

  function schedule() {
    const linkedCid = detail().hasLinkedEpisode ? reference.value?.cid : undefined
    const priority = new Set(toCheck.value.map(episode => episode.cid))
    for (const ep of [reference.value, ...nearEpisode(episodes(), linkedCid)]) {
      if (!ep?.cid || ep.cid < 0 || results.has(ep.cid)) continue
      const cached = cachedPlayback(playbackKey(ep, detail(), account()))?.result
      if (cached) {
        results.set(ep.cid, cached)
        continue
      }
      if (ep.cid !== reference.value?.cid && !priority.has(ep.cid)) continue
      results.set(ep.cid, { access: checking })
      queue.push(ep)
    }
    pump()
  }

  watch([episodes, toCheck], schedule, { immediate: true })
  watch([detail, account], () => {
    generation++
    queue = []
    results.clear()
    schedule()
  }, { flush: 'sync' })
  onScopeDispose(() => {
    disposed = true
    queue = []
  })

  return { referenceQualities, accessFor, canSelect, qualitiesFor: (cid: number) => results.get(cid)?.qualities }
}
