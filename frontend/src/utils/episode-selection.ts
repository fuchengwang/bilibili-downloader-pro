type Episode = { cid: number; index: number; title: string; longTitle?: string }
export type EpisodeSelection = Record<number, boolean>

export function nearEpisode<T extends { cid: number }>(episodes: T[], cid?: number): T[] {
  if (!cid) return episodes
  const current = episodes.findIndex(episode => episode.cid === cid)
  if (current < 0) return episodes

  const ordered = [episodes[current]]
  for (let distance = 1; current - distance >= 0 || current + distance < episodes.length; distance++) {
    if (current + distance < episodes.length) ordered.push(episodes[current + distance])
    if (current - distance >= 0) ordered.push(episodes[current - distance])
  }
  return ordered
}

export function filterEpisodes<T extends Episode>(episodes: T[], query: string): T[] {
  const text = query.trim().toLocaleLowerCase()
  if (!text) return episodes
  if (/^\d+$/.test(text)) return episodes.filter(ep => ep.index === Number(text) || `${ep.title} ${ep.longTitle || ''}`.toLocaleLowerCase().includes(text))
  const sequence = text.replace(/，/g, ',')
  if (/^\d+(?:\s*-\s*\d+)?(?:\s*,\s*\d+(?:\s*-\s*\d+)?)*$/.test(sequence)) {
    const ranges = sequence.split(',').map(item => item.split('-').map(Number))
    return episodes.filter(ep => ranges.some(([start, end = start]) => start <= end && ep.index >= start && ep.index <= end))
  }
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
