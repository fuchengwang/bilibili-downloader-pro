import { computed, ref, watch } from 'vue'
import { CheckHistoryFiles } from '../../wailsjs/go/main/App'
import type { bilibili, downloader } from '../../wailsjs/go/models'

function highestQuality(options: bilibili.QualityOption[] | undefined, codec: string) {
  const target = codec.toLowerCase()
  return (options || []).reduce((best, q) => {
    const matches = !target || target === 'auto' || q.codecs?.toLowerCase().split(',').includes(target)
    return q.isAvailable && matches ? Math.max(best, q.id) : best
  }, 0)
}

export function expectedEpisodeQualities(episodes: bilibili.EpisodeInfo[], quality: string, codec: string,
  qualitiesFor: (cid: number) => bilibili.QualityOption[] | undefined) {
  if (quality !== 'highest') return undefined
  const result: Record<number, number> = {}
  for (const ep of episodes) {
    const quality = highestQuality(qualitiesFor(ep.cid), codec)
    if (quality) result[ep.cid] = quality
  }
  return result
}

export function useDownloadedEpisodes(
  episodes: () => bilibili.EpisodeInfo[],
  history: () => downloader.DownloadTask[],
  type: () => string,
  quality: () => string,
  codec: () => string,
  qualitiesFor: (cid: number) => bilibili.QualityOption[] | undefined,
) {
  const candidates = computed(() => {
    const cids = new Set(episodes().map(episode => episode.cid))
    return history().filter(task => task.status === 'completed' && task.qualityId > 0 && task.outputPath &&
      Boolean(task.isBangumi) === (type() === 'bangumi') && Boolean(task.isCheese) === (type() === 'cheese') && cids.has(task.cid))
  })
  const fileStates = ref<Record<string, string>>({})
  watch(() => JSON.stringify([...new Set(candidates.value.map(task => task.outputPath))]), (pathList, _, onCleanup) => {
    const paths: string[] = JSON.parse(pathList)
    if (!paths.length) return
    let current = true
    const timer = setTimeout(() => {
      void CheckHistoryFiles(paths).then(result => {
        if (current) fileStates.value = result
      }).catch(() => {})
    }, 30)
    onCleanup(() => { current = false; clearTimeout(timer) })
  }, { immediate: true })

  const downloaded = computed(() => {
    const result = new Set<number>()
    const targetCodec = codec().toLowerCase()
    for (const task of candidates.value) {
      if (fileStates.value[task.outputPath] !== 'present') continue
      if (targetCodec && targetCodec !== 'auto' && task.codec?.toLowerCase() !== targetCodec) continue
      let targetQuality = Number(quality())
      if (quality() === 'highest') {
        targetQuality = highestQuality(qualitiesFor(task.cid), targetCodec)
      }
      if (targetQuality === task.qualityId) result.add(task.cid)
    }
    return result
  })
  return (cid: number) => downloaded.value.has(cid)
}
