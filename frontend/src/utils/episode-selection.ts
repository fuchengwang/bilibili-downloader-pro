type Episode = { cid: number; index: number; title: string; longTitle?: string }
export type EpisodeSelection = Record<number, boolean>

export function filterEpisodes<T extends Episode>(episodes: T[], query: string): T[] {
  const text = query.trim().toLocaleLowerCase()
  if (!text) return episodes
  const number = text.match(/^(?:p|第)?(\d+)(?:集|话|课|p)?$/i)
  if (number) return episodes.filter(ep => ep.index === Number(number[1]))
  const words = text.split(/\s+/)
  return episodes.filter(ep => {
    const title = `${ep.title} ${ep.longTitle || ''}`.toLocaleLowerCase()
    return words.every(word => title.includes(word))
  })
}

export function selectEpisodes(selection: EpisodeSelection, episodes: Episode[], invert = false): EpisodeSelection {
  const next = { ...selection }
  for (const ep of episodes) next[ep.cid] = invert ? !selection[ep.cid] : true
  return next
}

export function episodeURL(ep: { bvid?: string; aid?: number; epid?: number; page?: number }, type: string): string {
  if (ep.epid && (type === 'bangumi' || type === 'cheese')) {
    return `https://www.bilibili.com/${type === 'cheese' ? 'cheese' : 'bangumi'}/play/ep${ep.epid}`
  }
  const video = ep.bvid || (ep.aid ? `av${ep.aid}` : '')
  if (!video) return ''
  return `https://www.bilibili.com/video/${encodeURIComponent(video)}/${ep.page && ep.page > 1 ? `?p=${ep.page}` : ''}`
}

export function isPermissionError(kind?: string): boolean {
  return ['charge_required', 'login_required', 'vip_required', 'purchase_required', 'preview_only', 'region_restricted'].includes(kind || '')
}
