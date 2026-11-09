<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import {
  Layers,
  X,
  CheckSquare,
  Square,
  Clock,
  Download,
  Search,
  ChevronRight,
  MinusSquare
} from 'lucide-vue-next'
import { bilibili } from '../../wailsjs/go/models'
import { GetPlaybackInfo } from '../../wailsjs/go/main/App'
import { getCollectionVideoParts } from '../utils/collection-parts-cache'
import { filterEpisodes, selectEpisodes } from '../utils/episode-selection'
import CollectionPartsPanel from './CollectionPartsPanel.vue'

const props = defineProps<{
  detail: bilibili.VideoDetail
  initialQuality: string
  initialCodec: string
  submitting?: boolean
  userInfo?: bilibili.UserInfo | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', cids: number[], quality: string, codec: string, videos: string[]): void
}>()

const targetQuality = ref(props.initialQuality || 'highest')
const targetCodec = ref(props.initialCodec || 'auto')
const availableQualities = ref<bilibili.QualityOption[]>([])
const selectedMap = ref<Record<number, boolean>>({})
const rangeInput = ref('')
const searchQuery = ref('')
const showSelectedOnly = ref(false)
const locating = ref(false)
let locateTimer: ReturnType<typeof setTimeout> | undefined
const episodeGrid = ref<HTMLElement | null>(null)
const playbackError = ref<bilibili.PlaybackError | null>(null)
const groups = ref<Record<string, bilibili.CollectionVideo>>({})
const activeVideoID = ref('')
const activeVideo = computed(() => groups.value[activeVideoID.value])
const loading = ref(false)
const loadError = ref('')
const groupErrors = ref<Record<string, string>>({})
const selectionIncomplete = computed(() => props.detail.collectionIncomplete || loading.value || Object.keys(groupErrors.value).length > 0)
const loadedCount = ref(0)
let disposed = false
onUnmounted(() => { disposed = true; clearTimeout(locateTimer); qualityRequest++ })
const linkedEpisode = computed(() => props.detail.hasLinkedEpisode ? props.detail.episodes[(props.detail.defaultPage || 1) - 1] : undefined)
const filteredEpisodes = computed(() => {
  const q = searchQuery.value.trim().toLocaleLowerCase()
  if (!q) return props.detail.episodes
  const titleMatches = new Set(filterEpisodes(props.detail.episodes, q).map(ep => ep.bvid))
  if (/^(?:(?:p|第)\d+(?:集|话|课|p)?|\d+(?:集|话|课|p)|\d+(?:\s*[-,，]\s*\d+)+)$/i.test(q)) return props.detail.episodes.filter(ep => titleMatches.has(ep.bvid))
  return props.detail.episodes.filter(ep => titleMatches.has(ep.bvid) || (groups.value[ep.bvid]?.parts || []).some(part => part.title.toLocaleLowerCase().includes(q)))
})
const visibleEpisodes = computed(() => showSelectedOnly.value ? props.detail.episodes.filter(ep => selectedCount(ep) > 0) : filteredEpisodes.value)
const visibleParts = computed(() => visibleEpisodes.value.flatMap(partsFor))
const visibleSelected = computed(() => visibleParts.value.filter(ep => selectedMap.value[ep.cid]).length)
const allVisibleSelected = computed(() => visibleParts.value.length > 0 && visibleSelected.value === visibleParts.value.length)
const hiddenSelectedCount = computed(() => {
  const visibleCids = new Set(visibleEpisodes.value.flatMap(ep => partsFor(ep).map(part => part.cid)))
  return selectedList.value.filter(ep => !visibleCids.has(ep.cid)).length
})
const selectionScope = computed(() => searchQuery.value.trim() || showSelectedOnly.value ? '结果' : '全部')
watch(() => props.userInfo, loadQualities)

async function locateCurrentEpisode(highlight = true) {
  searchQuery.value = ''
  showSelectedOnly.value = false
  await nextTick()
  const card = episodeGrid.value?.querySelector<HTMLElement>(`[data-cid="${linkedEpisode.value?.cid}"]`)
  card?.scrollIntoView({ block: 'center', behavior: highlight ? 'smooth' : 'auto' })
  if (highlight && card) {
    clearTimeout(locateTimer)
    locating.value = false
    await nextTick()
    locating.value = true
    locateTimer = setTimeout(() => { locating.value = false }, 2600)
  }
}

onMounted(async () => {
  // 默认全部不勾选，由用户按需勾选
  selectedMap.value = {}
  loadQualities()
  await locateCurrentEpisode(false)
  if (!disposed) await loadGroups()
})

async function loadGroups() {
  if (loading.value || disposed) return
  loading.value = true
  loadError.value = ''
  loadedCount.value = props.detail.episodes.filter(ep => groups.value[ep.bvid]).length
  try {
    let next = 0
    let firstError: unknown
    const worker = async () => {
      while (!disposed && next < props.detail.episodes.length) {
        const ep = props.detail.episodes[next++]
        if (groups.value[ep.bvid]) continue
        try {
          const group = await getCollectionVideoParts(ep)
          if (disposed) return
          groups.value[ep.bvid] = group
          delete groupErrors.value[ep.bvid]
          loadedCount.value++
        } catch (err) {
          if (disposed) return
          groupErrors.value[ep.bvid] = String((err as any)?.message || err)
          firstError ||= err
        }
      }
    }
    await Promise.all(Array.from({ length: Math.min(4, props.detail.episodes.length) }, worker))
    if (firstError) throw firstError
  } catch (err: any) {
    loadError.value = String(err?.message || err)
  } finally {
    loading.value = false
  }
}

async function retryGroup(ep: bilibili.EpisodeInfo) {
  if (loading.value || !groupErrors.value[ep.bvid]) return
  // Reuse successful groups and retry only failed reads.
  await loadGroups()
}

function partsFor(ep: bilibili.EpisodeInfo): bilibili.EpisodeInfo[] {
  return groups.value[ep.bvid]?.parts || []
}
function selectedCount(ep: bilibili.EpisodeInfo) {
  return partsFor(ep).filter(part => selectedMap.value[part.cid]).length
}
function openParts(ep: bilibili.EpisodeInfo) {
  if (groups.value[ep.bvid]) activeVideoID.value = ep.bvid
}

function toggleGroup(ep: bilibili.EpisodeInfo) {
  const parts = partsFor(ep)
  if (!parts.length) return
  const next = selectedCount(ep) !== parts.length
  for (const part of parts) selectedMap.value[part.cid] = next
}

let qualityRequest = 0
async function loadQualities() {
  const request = ++qualityRequest
  playbackError.value = null
  availableQualities.value = []
  // 加载画质列表
  if (props.detail.episodes && props.detail.episodes.length > 0) {
    try {
      const ep0 = props.detail.episodes[Math.max(0, (props.detail.defaultPage || 1) - 1)] || props.detail.episodes[0]
      const info = await GetPlaybackInfo(
        ep0.bvid || props.detail.bvid,
        ep0.aid || props.detail.aid,
        ep0.cid,
        ep0.epid || 0,
        props.detail.type === 'bangumi',
        props.detail.type === 'cheese'
      )
      if (request !== qualityRequest) return
      playbackError.value = info?.error || null
      const qList = info?.qualities || []
      if (qList) {
        availableQualities.value = qList
        if (targetQuality.value !== 'highest') {
          const currentOpt = qList.find(q => q.id.toString() === targetQuality.value)
          if (currentOpt && !currentOpt.isAvailable) {
            targetQuality.value = 'highest'
          }
        }
      }
    } catch (e: any) {
      if (request === qualityRequest) playbackError.value = { kind: 'unavailable', message: String(e?.message || e), hint: '请稍后重新检查。' }
    }
  }
}

const selectedList = computed(() => {
  return props.detail.episodes.flatMap(partsFor).filter(ep => selectedMap.value[ep.cid])
})

const canSubmit = computed(() => selectedList.value.length > 0 && !props.submitting)

function toggleEpisode(cid: number) {
  selectedMap.value[cid] = !selectedMap.value[cid]
}

function selectAll() {
  if (allVisibleSelected.value) {
    const next = { ...selectedMap.value }
    for (const part of visibleParts.value) delete next[part.cid]
    selectedMap.value = next
  } else selectedMap.value = selectEpisodes(selectedMap.value, visibleParts.value)
}

function deselectAll() {
  selectedMap.value = {}
}

function toggleSelectedView() {
  if (activeVideoID.value) {
    activeVideoID.value = ''
    searchQuery.value = ''
    showSelectedOnly.value = true
  } else {
    if (!showSelectedOnly.value) searchQuery.value = ''
    showSelectedOnly.value = !showSelectedOnly.value
  }
}

function invertSelection() {
  selectedMap.value = selectEpisodes(selectedMap.value, visibleEpisodes.value.flatMap(partsFor), true)
}

// 区间快速选择，如 "1-10" 或 "1,3,5-8"
function applyRangeSelection() {
  const raw = rangeInput.value.trim()
  if (!raw) return

  const map: Record<number, boolean> = { ...selectedMap.value }
  const parts = raw.split(',')

  parts.forEach(part => {
    part = part.trim()
    if (part.includes('-')) {
      const [startStr, endStr] = part.split('-')
      const start = parseInt(startStr, 10)
      const end = parseInt(endStr, 10)
      if (!isNaN(start) && !isNaN(end)) {
        props.detail.episodes.forEach(ep => {
          if (ep.index >= start && ep.index <= end) {
            partsFor(ep).forEach(part => { map[part.cid] = true })
          }
        })
      }
    } else {
      const num = parseInt(part, 10)
      if (!isNaN(num)) {
        props.detail.episodes.forEach(ep => {
          if (ep.index === num) {
            partsFor(ep).forEach(part => { map[part.cid] = true })
          }
        })
      }
    }
  })

  selectedMap.value = map
  rangeInput.value = ''
}

function handleSubmit() {
  const cids = selectedList.value.map(ep => ep.cid)
  if (!canSubmit.value || props.submitting) return
  emit('submit', cids, targetQuality.value, targetCodec.value, props.detail.episodes.filter(ep => selectedCount(ep) > 0).map(ep => ep.bvid))
}
</script>

<template>
  <div class="modal-overlay" @click.self="emit('close')">
    <div class="modal-content episode-modal">
      <!-- Modal Header -->
      <div class="modal-header">
        <div class="header-title-box">
          <Layers class="header-icon" :size="20" />
          <div class="title-col">
            <h2 class="title-text">{{ detail.type === 'cheese' ? '课时选择与批量下载' : '分集选择与批量下载' }}</h2>
            <span class="sub-text">{{ detail.collectionTitle || detail.title }} (共 {{ detail.totalParts }} {{ detail.type === 'cheese' ? '课时' : '集' }})</span>
          </div>
        </div>
        <button class="btn-icon close-btn" @click="emit('close')" title="关闭">
          <X :size="18" />
        </button>
      </div>

      <!-- Selection & Options Toolbar -->
      <div v-if="!activeVideo" class="toolbar collection-toolbar">
        <div class="search-row">
          <label class="episode-search">
            <Search :size="15" />
            <input v-model="searchQuery" type="search" placeholder="搜索标题、集号或范围，如 美学、P8、1-10" aria-label="搜索分集标题或集号" @input="showSelectedOnly = false" />
            <button v-if="searchQuery" class="search-clear" @click="searchQuery = ''" aria-label="清除搜索"><X :size="14" /></button>
          </label>
          <button v-if="linkedEpisode" class="locate-button" :title="`定位第 ${linkedEpisode.index} 集：${linkedEpisode.videoTitle || linkedEpisode.title}`" @click="locateCurrentEpisode()">定位当前集</button>
        </div>
        <div class="format-row">
        <div class="tool-options">
          <div class="opt-item">
            <span class="opt-label">画质</span>
            <select v-model="targetQuality" class="opt-select" title="自动模式会按各视频的可用画质分别选择" aria-label="下载画质">
              <option value="highest">按可用画质自动选择</option>
              <option
                v-for="q in availableQualities"
                :key="q.id"
                :value="String(q.id)"
                :disabled="!q.isAvailable"
              >
                {{ q.label }} {{ q.isVipRequired ? '(大会员)' : (q.isLoginRequired ? '(需登录)' : '') }}
              </option>
            </select>
          </div>

          <div class="opt-item"><span class="opt-label">编码</span><select v-model="targetCodec" class="opt-select" aria-label="视频编码"><option value="auto">自动</option><option value="AVC">AVC / H.264</option><option value="HEVC">HEVC / H.265</option><option value="AV1">AV1</option></select></div>
        </div>
        </div>
        <div class="selection-row">
        <div class="tool-actions">
          <button class="tool-btn" role="checkbox" :aria-checked="allVisibleSelected ? 'true' : visibleSelected ? 'mixed' : 'false'" :disabled="!visibleParts.length || submitting" @click="selectAll">
            <CheckSquare v-if="allVisibleSelected" :size="14" /><MinusSquare v-else-if="visibleSelected" :size="14" /><Square v-else :size="14" />
            <span>{{ allVisibleSelected ? '取消全选' : '全选' }}{{ searchQuery || showSelectedOnly ? '结果' : selectionIncomplete ? '已读取' : '' }}</span>
          </button>
          <button class="tool-btn" :disabled="!visibleParts.length || submitting" @click="invertSelection">{{ searchQuery || showSelectedOnly ? '反选结果' : '反选' }}</button>
          <button v-if="showSelectedOnly && selectedList.length" class="tool-btn" @click="deselectAll">清空全部</button>
        </div>
        </div>
        <p v-if="playbackError" class="quality-note access-note">{{ linkedEpisode ? '当前链接' : '首集' }}：{{ playbackError.message }}。其他分集按各自观看权限检查。</p>
        <p v-if="detail.collectionIncomplete" class="quality-note">合集列表未完整返回，可下载当前已读取的视频。</p>
        <p v-if="loading" class="quality-note">正在读取各视频的分 P：{{ loadedCount }} / {{ detail.episodes.length }}</p>
        <p v-else-if="loadError" class="quality-note access-note">部分视频读取失败，已读取的视频仍可下载。 <button class="text-action" @click="loadGroups">重新读取</button></p>
      </div>

      <!-- Episode Grid View -->
      <CollectionPartsPanel v-if="activeVideo" :key="activeVideo.bvid" :video="activeVideo" :selection="selectedMap" @back="activeVideoID = ''" @select="selectedMap = $event" />
      <div v-if="!activeVideo && showSelectedOnly" class="selected-view-heading">
        <div><strong>已选文件</strong><span>共 {{ selectedList.length }} 个文件</span></div>
        <button @click="showSelectedOnly = false">返回全部分集</button>
      </div>
      <div v-if="!activeVideo" ref="episodeGrid" class="episode-grid">
        <div
          v-for="ep in visibleEpisodes"
          :key="`${ep.index}-${ep.bvid}`"
          class="ep-card collection-card"
          :class="{ selected: selectedCount(ep) > 0, 'current-episode': ep.bvid === linkedEpisode?.bvid, 'locate-highlight': locating && ep.bvid === linkedEpisode?.bvid }"
          :data-cid="ep.cid"
        >
          <div class="collection-main" role="checkbox" :aria-checked="selectedCount(ep) === 0 ? 'false' : selectedCount(ep) === partsFor(ep).length ? 'true' : 'mixed'" :aria-label="`选择第${ep.index}集 ${ep.videoTitle || ep.title}`" :aria-disabled="!partsFor(ep).length" :tabindex="partsFor(ep).length ? 0 : -1" @click="toggleGroup(ep)" @keydown.space.prevent="toggleGroup(ep)" @keydown.enter.prevent="toggleGroup(ep)">
          <div class="ep-check">
            <CheckSquare v-if="partsFor(ep).length && selectedCount(ep) === partsFor(ep).length" :size="16" class="checked-icon" />
            <MinusSquare v-else-if="selectedCount(ep)" :size="16" class="checked-icon" />
            <Square v-else :size="16" class="unchecked-icon" />
          </div>

          <div class="ep-content">
            <div class="ep-top">
              <span class="ep-index">第 {{ ep.index }} 集</span>
              <span v-if="ep.bvid === linkedEpisode?.bvid" class="current-badge">当前链接</span>
              <span v-if="ep.badge" class="badge badge-pink ep-badge">{{ ep.badge }}</span>
            </div>
            <div class="ep-title" :title="ep.videoTitle || ep.title">{{ ep.videoTitle || ep.title }}</div>
          </div>
          </div>
          <div class="collection-card-footer" :class="{ 'has-parts': partsFor(ep).length > 1 }">
            <span class="ep-duration"><Clock :size="11" />{{ ep.durationStr }}</span>
            <button v-if="groupErrors[ep.bvid]" class="text-action" :title="groupErrors[ep.bvid]" :disabled="loading" @click="retryGroup(ep)">读取失败 · 重试</button>
            <span v-else-if="!groups[ep.bvid]" class="video-part-count">读取中…</span>
            <button v-else-if="partsFor(ep).length > 1" class="parts-toggle" @click="openParts(ep)">{{ partsFor(ep).length }} 个分 P<span v-if="selectedCount(ep)"> · 已选 {{ selectedCount(ep) }}</span><ChevronRight :size="13" /></button>
          </div>
        </div>
        <div v-if="visibleEpisodes.length === 0" class="no-episodes"><Search :size="24" /><p>{{ showSelectedOnly ? '还没有选择分集' : '没有找到匹配的分集' }}</p><button v-if="searchQuery || showSelectedOnly" class="text-action" @click="searchQuery = ''; showSelectedOnly = false">显示全部分集</button></div>
      </div>

      <!-- Modal Footer -->
      <div class="modal-footer">
        <div class="footer-stats">
          <button v-if="selectedList.length || showSelectedOnly" class="selected-link" @click="toggleSelectedView">{{ showSelectedOnly && !activeVideo ? '返回全部' : '已选 ' + selectedList.length + ' 个文件 ›' }}</button>
          <span v-else>已选 <span class="highlight-num">0</span> 个文件</span>
        </div>

        <div class="footer-actions">
          <button class="btn-secondary cancel-btn" @click="emit('close')">取消</button>
          <button
            class="btn-primary start-btn"
            :disabled="!canSubmit"
            @click="handleSubmit"
          >
            <Download :size="15" />
            <span>{{ submitting ? '正在添加…' : '开始下载' }}</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.episode-modal {
  width: 760px;
  max-width: 90vw;
  height: min(680px, 85vh);
  display: flex;
  flex-direction: column;
}

.modal-header {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border-subtle);
}

.header-title-box {
  display: flex;
  align-items: center;
  gap: 10px;
}

.header-icon {
  color: var(--bili-pink);
}

.title-col {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.title-text {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
}

.sub-text {
  font-size: 11.5px;
  color: var(--text-muted);
  max-width: 500px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.toolbar {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 20px;
  background: var(--neutral-02);
  border-bottom: 1px solid var(--border-subtle);
  flex-wrap: wrap;
  gap: 10px;
}

.search-row, .selection-row, .current-link-row { display: flex; width: 100%; align-items: center; justify-content: space-between; gap: 10px; }
.episode-search { display: flex; flex: 1; align-items: center; gap: 8px; height: 34px; padding: 0 10px; border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); background: var(--bg-card); color: var(--text-muted); }
.episode-search:focus-within { border-color: var(--bili-pink); }
.episode-search input { flex: 1; min-width: 0; height: 100%; appearance: none; -webkit-appearance: none; background: transparent; border: 0; border-radius: 0; box-shadow: none; padding: 0; outline: none; font-size: 12px; }
.episode-search input::-webkit-search-cancel-button { display: none; }
.search-clear { display: flex; color: var(--text-muted); background: transparent; }
.search-count { color: var(--text-muted); font-size: 11.5px; flex-shrink: 0; min-width: 90px; text-align: right; font-variant-numeric: tabular-nums; }
.current-link-row { font-size: 11.5px; color: var(--text-secondary); }
.current-link-row > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.text-action { color: var(--bili-pink); font-size: 11.5px; flex-shrink: 0; background: transparent; }
.selection-row { flex-wrap: wrap; }
.advanced-options { display: grid; width: 100%; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 14px; padding: 14px; background: var(--bg-card); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); }
.advanced-group { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; min-width: 0; }
.advanced-label { font-size: 12px; color: var(--text-primary); font-weight: 600; }
.advanced-label span { display: block; margin-top: 3px; color: var(--text-muted); font-size: 10.5px; font-weight: 400; }
.advanced-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 7px; }
.advanced-group .range-input { width: 125px; }
.tool-btn.active { color: var(--bili-pink); border-color: var(--bili-pink); }
.tool-btn:disabled { opacity: .45; cursor: default; }
.ep-card.current-episode { border-color: rgba(251, 114, 153, .35); }
.ep-card:focus-visible { outline: 2px solid var(--bili-pink); outline-offset: 2px; }
.start-btn:disabled { opacity: .45; cursor: not-allowed; box-shadow: none; }
.current-badge { margin-left: 5px; color: var(--bili-pink); font-size: 9px; white-space: nowrap; }
.no-episodes { grid-column: 1 / -1; min-height: 160px; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px; color: var(--text-muted); font-size: 12px; }
.hidden-selected { display: block; margin-top: 4px; color: var(--warning); font-size: 11px; }
.access-note { color: var(--warning); }

.tool-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.tool-btn {
  height: 28px;
  padding: 0 10px;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-xs);
  color: var(--text-secondary);
  font-size: 11.5px;
  display: flex;
  align-items: center;
  gap: 4px;
}

.tool-btn:hover {
  background: var(--bg-card-hover);
  color: var(--text-primary);
}

.range-box {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-left: 6px;
}

.range-input {
  width: 100px;
  height: 28px;
  padding: 0 8px;
  font-size: 11.5px;
  border-radius: var(--radius-xs);
}

.range-btn {
  height: 28px;
  padding: 0 8px;
  font-size: 11.5px;
}

.quality-note {
  margin: 10px 0 0;
  color: var(--text-secondary);
  font-size: 12px;
  line-height: 1.6;
}

.tool-options {
  display: flex;
  align-items: center;
  gap: 12px;
}

.opt-item {
  display: flex;
  align-items: center;
  gap: 6px;
}

.opt-label {
  font-size: 12px;
  color: var(--text-secondary);
}

.opt-select {
  height: 28px;
  padding: 0 8px;
  font-size: 11.5px;
  min-width: 160px;
}

.opt-select option:disabled {
  color: var(--text-muted);
  background: var(--bg-badge);
}

.episode-grid {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 16px 20px;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 10px;
  align-content: start;
}

.episode-grid:has(.no-episodes) {
  align-content: stretch;
}

.ep-card {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 10px;
  border-radius: var(--radius-sm);
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  cursor: pointer;
  transition: all var(--transition-fast);
}

.ep-card:hover {
  background: var(--bg-card-hover);
  border-color: var(--neutral-15);
}

.ep-card.selected {
  background: rgba(251, 114, 153, 0.08);
  border-color: rgba(251, 114, 153, 0.4);
}

.episode-grid { align-items: start; grid-auto-rows: 102px; }
.collection-card { display: grid; grid-template-rows: minmax(0, 1fr) 26px; height: 102px; min-height: 102px; min-width: 0; gap: 0; padding: 0; cursor: default; overflow: hidden; }
.collection-main { display: flex; align-items: flex-start; gap: 8px; min-width: 0; min-height: 0; width: 100%; padding: 10px; cursor: pointer; }
.collection-main:focus-visible { outline: 2px solid var(--bili-pink); outline-offset: -2px; }
.collection-card-footer { display: flex; align-items: center; justify-content: space-between; gap: 6px; width: 100%; min-width: 0; padding: 0 10px; }
.collection-card-footer .ep-duration { margin-left: 0; }
.collection-card-footer.has-parts { background: transparent; }
.video-part-count { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: 10.5px; }
.parts-toggle { display: flex; align-items: center; gap: 3px; flex-shrink: 0; height: 22px; padding: 0 6px; border-radius: var(--radius-xs); background: rgba(251, 114, 153, .08); color: var(--bili-pink); font-size: 11px; }
.subtle-action { border-color: transparent; background: transparent; }
.parts-toggle:hover { color: var(--bili-pink-hover); }
.parts-toggle:focus-visible { outline: 2px solid var(--bili-pink); outline-offset: 1px; }

.ep-check {
  margin-top: 1px;
  flex-shrink: 0;
}

.checked-icon {
  color: var(--bili-pink);
}

.unchecked-icon {
  color: var(--text-muted);
}

.ep-content {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 4px;
  overflow: hidden;
}

.ep-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.ep-index {
  font-size: 11px;
  font-weight: 700;
  color: var(--bili-pink);
}

.ep-badge {
  font-size: 9px;
  padding: 0 4px;
}

.ep-duration {
  display: flex;
  align-items: center;
  gap: 3px;
  font-size: 10.5px;
  color: var(--text-muted);
  margin-left: auto;
}

.ep-title {
  font-size: 12px;
  color: var(--text-primary);
  line-height: 1.3;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.modal-footer {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 20px;
  border-top: 1px solid var(--border-subtle);
  background: var(--neutral-015);
}

.footer-stats {
  font-size: 12.5px;
  color: var(--text-secondary);
}

.highlight-num {
  color: var(--bili-pink);
  font-weight: 700;
}

.footer-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.cancel-btn, .add-queue-btn, .start-btn {
  height: 32px;
  padding: 0 14px;
  font-size: 12.5px;
  gap: 4px;
}

/* Two aligned rows: find / format, then selection actions. */
.collection-toolbar { display: grid; grid-template-columns: minmax(180px, 1fr) auto; align-items: center; gap: 14px 24px; padding: 18px 24px 14px; background: transparent; }
.search-row { width: auto; min-width: 0; }
.episode-search { min-width: 0; height: 34px; border-radius: 7px; background: var(--bg-input); }
.locate-button { flex-shrink: 0; height: 34px; padding: 0 10px; border: 1px solid var(--border-subtle); border-radius: 7px; color: var(--text-secondary); font-size: 11px; background: var(--bg-card); white-space: nowrap; }
.locate-button:hover { color: var(--bili-pink); }
.format-row { min-width: 0; }
.tool-options { display: flex; align-items: center; gap: 16px; }
.opt-item { gap: 6px; }
.opt-label { font-size: 11px; color: var(--text-muted); white-space: nowrap; }
.opt-select { min-width: 0; width: 120px; height: 34px; border-radius: 6px; font-size: 11px; background: var(--bg-card); }
.opt-item:last-child .opt-select { width: 112px; }
.selection-row { grid-column: 1 / -1; justify-content: space-between; gap: 10px 16px; }
.tool-actions { flex: 1 1 300px; min-width: 0; }
.tool-actions { gap: 4px; }
.tool-btn { height: 30px; border: 0; background: transparent; padding: 0 8px; font-size: 11.5px; color: var(--text-secondary); }
.tool-actions .tool-btn:first-child { padding-left: 0; padding-right: 14px; margin-right: 4px; border-right: 1px solid var(--border-subtle); border-radius: 0; color: var(--text-primary); font-weight: 500; }
.sequence-select { flex: 0 0 190px; min-width: 190px; box-sizing: border-box; display: flex; align-items: center; height: 30px; border: 1px solid var(--border-subtle); border-radius: 6px; overflow: hidden; background: var(--bg-input); }
.sequence-select:focus-within { border-color: var(--bili-pink); }
.sequence-select input { flex: 1 1 0; width: 0; min-width: 0; height: 100%; border: 0; border-radius: 0; background: transparent; padding: 0 9px; font-size: 11px; outline: none; box-shadow: none; }
.sequence-select button { flex: 0 0 auto; appearance: none; box-shadow: none; border-radius: 0; height: 100%; border: 0; border-left: 1px solid var(--border-subtle); padding: 0 10px; background: var(--bg-card); color: var(--text-primary); font-size: 11px; }
.sequence-select button:disabled { opacity: .45; cursor: default; }
.quality-note { grid-column: 1 / -1; margin: 0; font-size: 11px; }
.episode-grid { padding: 18px 24px; gap: 12px; }
.ep-index { color: var(--text-muted); font-weight: 500; }
.ep-title { font-size: 12.5px; line-height: 1.5; }
.modal-footer { background: transparent; }
.text-action { color: var(--text-secondary); font-size: 11px; }
.text-action:hover { color: var(--bili-pink); }
.selected-link { background: transparent; color: var(--bili-pink); font-size: 12px; padding: 6px 0; }
.selected-link:hover { text-decoration: underline; }
.selected-view-heading { flex-shrink: 0; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 24px; background: rgba(251, 114, 153, .07); border-bottom: 1px solid var(--border-subtle); }
.selected-view-heading > div { display: flex; align-items: baseline; gap: 10px; }
.selected-view-heading strong { font-size: 12px; color: var(--text-primary); }
.selected-view-heading span { font-size: 11px; color: var(--text-secondary); }
.selected-view-heading button { border: 0; padding: 4px 0; background: transparent; color: var(--bili-pink); font-size: 11px; }
.collection-card.locate-highlight { border-color: var(--bili-pink); box-shadow: inset 0 0 0 1px var(--bili-pink), 0 0 0 4px rgba(251, 114, 153, .2); background: rgba(251, 114, 153, .1); }
@media (prefers-reduced-motion: reduce) { .collection-card { transition: none; } }
.episode-search, .locate-button, .opt-select, .sequence-select { height: 30px; box-sizing: border-box; }
.episode-search, .sequence-select, .opt-select { background: var(--bg-card); }
.episode-search input, .locate-button, .opt-label, .opt-select, .tool-btn, .sequence-select input, .sequence-select button { font-size: 11.5px; }
.selection-row, .tool-options, .opt-item, .tool-actions { align-items: center; }
.tool-actions { gap: 4px; }
.tool-actions .tool-btn:first-child { position: relative; padding: 0 8px; margin-right: 12px; border: 0; border-radius: 5px; }
.tool-actions .tool-btn:first-child::after { content: ''; position: absolute; right: -8px; top: 7px; width: 1px; height: 16px; background: var(--border-subtle); pointer-events: none; }
.collection-main { display: grid; grid-template-columns: 16px minmax(0, 1fr); grid-template-rows: 16px auto; column-gap: 8px; row-gap: 5px; align-content: start; }
.collection-main .ep-check { margin: 0; line-height: 0; }
.collection-main .ep-content { display: contents; }
.collection-main .ep-top { min-width: 0; height: 16px; }
.collection-main .ep-title { grid-column: 1 / -1; text-align: left; line-height: 1.45; }
@media (max-width: 700px) {
 .collection-toolbar { grid-template-columns: 1fr; gap: 10px; }
 .search-row, .selection-row { grid-column: 1; }
 .format-row { grid-row: 3; }
 .tool-options { justify-content: flex-end; }
}
</style>
