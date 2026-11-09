import { computed, onScopeDispose, shallowReactive, watch } from 'vue'
import { GetPlaybackInfo } from '../../wailsjs/go/main/App'
import type { bilibili } from '../../wailsjs/go/models'
import { isPermissionError, nearEpisode } from './episode-selection'

export type EpisodeAccess = {
  state: 'checking' | 'available' | 'blocked' | 'unknown' | 'partial'
  reason?: string
}

const checking: EpisodeAccess = { state: 'checking', reason: '正在检查观看权限' }
const unknown: EpisodeAccess = { state: 'unknown', reason: '暂时无法确认观看权限，可以尝试下载' }
const noVideo: EpisodeAccess = { state: 'blocked', reason: 'B 站未提供这一集的视频信息' }

export function useEpisodePlayback(
  detail: () => bilibili.VideoDetail,
  account: () => bilibili.UserInfo | null | undefined,
  episodes: () => bilibili.EpisodeInfo[]
) {
  const results = shallowReactive(new Map<number, { access: EpisodeAccess; qualities?: bilibili.QualityOption[] }>())
  const reference = computed(() => detail().episodes[Math.max(0, (detail().defaultPage || 1) - 1)] || detail().episodes[0])
  const referenceQualities = computed(() => results.get(reference.value?.cid)?.qualities || [])
  let generation = 0
  let disposed = false
  let running = 0
  let queue: bilibili.EpisodeInfo[] = []

  function accessFor(cid: number): EpisodeAccess {
    if (cid <= 0) return noVideo
    return results.get(cid)?.access || checking
  }

  function canSelect(cid: number) {
    const state = accessFor(cid).state
    return state === 'available' || state === 'unknown'
  }

  async function check(ep: bilibili.EpisodeInfo, request: number) {
    const video = detail()
    let access = unknown
    let qualities: bilibili.QualityOption[] | undefined
    try {
      const info = await GetPlaybackInfo(ep.bvid || video.bvid, ep.aid || video.aid, ep.cid, ep.epid || 0,
        video.type === 'bangumi', video.type === 'cheese')
      if (isPermissionError(info?.error?.kind)) {
        access = { state: 'blocked', reason: info.error?.message }
      } else if (!info?.error && info?.qualities?.some(quality => quality.isAvailable)) {
        access = { state: 'available' }
      } else {
        access = { state: 'unknown', reason: info?.error?.message || unknown.reason }
      }
      if (ep.cid === reference.value?.cid) qualities = info?.qualities
    } catch {
      // A failed request does not establish a viewing restriction.
    } finally {
      if (!disposed && request === generation) results.set(ep.cid, { access, qualities })
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
    for (const ep of [reference.value, ...nearEpisode(episodes(), linkedCid)]) {
      if (!ep?.cid || ep.cid < 0 || results.has(ep.cid)) continue
      results.set(ep.cid, { access: checking })
      queue.push(ep)
    }
    pump()
  }

  watch(episodes, schedule, { immediate: true })
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

  return { referenceQualities, accessFor, canSelect }
}
