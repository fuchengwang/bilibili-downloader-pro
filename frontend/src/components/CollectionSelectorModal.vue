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
import { getCollectionVideoParts } from '../utils/collection-parts-cache'
import { filterEpisodes, nearEpisode, selectEpisodes } from '../utils/episode-selection'
import { useEpisodePlayback, type EpisodeAccess } from '../utils/episode-playback'
import EpisodeSelectionMark from './EpisodeSelectionMark.vue'
import EpisodeSelectorToolbar from './EpisodeSelectorToolbar.vue'
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
const selectedMap = ref<Record<number, boolean>>({})
const searchQuery = ref('')
const showSelectedOnly = ref(false)
const episodeGrid = ref<HTMLElement | null>(null)
const groups = ref<Record<string, bilibili.CollectionVideo>>({})
const loadedParts = computed(() => props.detail.episodes.flatMap(partsFor))
const { referenceQualities: availableQualities, accessFor, canSelect } = useEpisodePlayback(
  () => props.detail, () => props.userInfo, () => loadedParts.value
)
const activeVideoID = ref('')
const activeVideo = computed(() => groups.value[activeVideoID.value])
const loading = ref(false)
const loadError = ref('')
const groupErrors = ref<Record<string, string>>({})
let disposed = false
onUnmounted(() => { disposed = true })
const linkedEpisode = computed(() => props.detail.hasLinkedEpisode ? props.detail.episodes[(props.detail.defaultPage || 1) - 1] : undefined)
const filteredEpisodes = computed(() => {
  const q = searchQuery.value.trim().toLocaleLowerCase()
  if (!q) return props.detail.episodes
  const titleMatches = new Set(filterEpisodes(props.detail.episodes, q).map(ep => ep.bvid))
  if (/^(?:(?:p|第)\d+(?:集|话|课|p)?|\d+(?:集|话|课|p)|\d+(?:\s*[-,，]\s*\d+)+)$/i.test(q)) return props.detail.episodes.filter(ep => titleMatches.has(ep.bvid))
  return props.detail.episodes.filter(ep => titleMatches.has(ep.bvid) || (groups.value[ep.bvid]?.parts || []).some(part => part.title.toLocaleLowerCase().includes(q)))
})
const visibleEpisodes = computed(() => showSelectedOnly.value ? props.detail.episodes.filter(ep => selectedCount(ep) > 0) : filteredEpisodes.value)
const visibleParts = computed(() => visibleEpisodes.value.flatMap(selectablePartsFor))
const visibleSelected = computed(() => visibleParts.value.filter(ep => selectedMap.value[ep.cid]).length)
const allVisibleSelected = computed(() => visibleParts.value.length > 0 && visibleSelected.value === visibleParts.value.length)
watch(searchQuery, () => { showSelectedOnly.value = false }, { flush: 'sync' })
watch(availableQualities, qualities => {
  if (qualities.some(q => String(q.id) === targetQuality.value && !q.isAvailable)) targetQuality.value = 'highest'
})

async function locateCurrentEpisode() {
  searchQuery.value = ''
  showSelectedOnly.value = false
  await nextTick()
  const card = episodeGrid.value?.querySelector<HTMLElement>(`[data-cid="${linkedEpisode.value?.cid}"]`)
  card?.scrollIntoView({ block: 'center' })
}

onMounted(async () => {
  // 默认全部不勾选，由用户按需勾选
  selectedMap.value = {}
  await locateCurrentEpisode()
  if (!disposed) await loadGroups()
})

async function loadGroups() {
  if (loading.value || disposed) return
  loading.value = true
  loadError.value = ''
  const episodes = nearEpisode(props.detail.episodes, linkedEpisode.value?.cid)
  try {
    let next = 0
    let firstError: unknown
    const worker = async () => {
      while (!disposed && next < episodes.length) {
        const ep = episodes[next++]
        if (groups.value[ep.bvid]) continue
        try {
          const group = await getCollectionVideoParts(ep)
          if (disposed) return
          groups.value[ep.bvid] = group
          delete groupErrors.value[ep.bvid]
        } catch (err) {
          if (disposed) return
          groupErrors.value[ep.bvid] = String((err as any)?.message || err)
          firstError ||= err
        }
      }
    }
    await Promise.all(Array.from({ length: Math.min(4, episodes.length) }, worker))
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
function selectablePartsFor(ep: bilibili.EpisodeInfo) {
  return partsFor(ep).filter(part => canSelect(part.cid))
}
function groupAccess(ep: bilibili.EpisodeInfo): EpisodeAccess {
  const statuses = partsFor(ep).map(part => accessFor(part.cid))
  const first = statuses[0]
  if (groupErrors.value[ep.bvid]) return { state: 'unknown', reason: '此视频读取失败' }
  if (!first || statuses.some(access => access.state === 'checking')) return { state: 'checking', reason: '正在检查分 P 的观看权限' }
  if (statuses.every(access => access.state === first.state)) return first
  if (statuses.some(access => access.state === 'blocked') && statuses.some(access => access.state === 'available')) {
    return { state: 'partial', reason: '此视频的部分分 P 不可下载，展开可查看各分 P 的权限' }
  }
  return { state: 'unknown', reason: '部分分 P 的权限未确认，展开可查看详情' }
}
const groupAccessByVideo = computed(() => Object.fromEntries(visibleEpisodes.value.map(ep => [ep.bvid, groupAccess(ep)])))
function selectedCount(ep: bilibili.EpisodeInfo) {
  return selectablePartsFor(ep).filter(part => selectedMap.value[part.cid]).length
}
function openParts(ep: bilibili.EpisodeInfo) {
  if (groups.value[ep.bvid]) activeVideoID.value = ep.bvid
}

function toggleGroup(ep: bilibili.EpisodeInfo) {
  const parts = selectablePartsFor(ep)
  if (!parts.length) return
  const next = selectedCount(ep) !== parts.length
  for (const part of parts) selectedMap.value[part.cid] = next
}

const selectedList = computed(() => {
  return loadedParts.value.filter(ep => canSelect(ep.cid) && selectedMap.value[ep.cid])
})

const canSubmit = computed(() => selectedList.value.length > 0 && !props.submitting)

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
  selectedMap.value = selectEpisodes(selectedMap.value, visibleParts.value, true)
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
          <h2 class="title-text" :title="detail.collectionTitle || detail.title">{{ detail.collectionTitle || detail.title }}</h2>
        </div>
        <button class="btn-icon close-btn" @click="emit('close')" title="关闭">
          <X :size="18" />
        </button>
      </div>

      <EpisodeSelectorToolbar
        v-if="!activeVideo"
        v-model:search="searchQuery"
        v-model:quality="targetQuality"
        v-model:codec="targetCodec"
        :qualities="availableQualities"
        :can-locate="!!linkedEpisode"
        @locate="locateCurrentEpisode"
      >
        <div class="tool-actions">
          <button class="tool-btn" role="checkbox" :aria-checked="allVisibleSelected ? 'true' : visibleSelected ? 'mixed' : 'false'" :disabled="!visibleParts.length || submitting" @click="selectAll">
            <CheckSquare v-if="allVisibleSelected" :size="14" /><MinusSquare v-else-if="visibleSelected" :size="14" /><Square v-else :size="14" />
            <span>{{ allVisibleSelected ? '取消全选' : '全选' }}{{ searchQuery || showSelectedOnly ? '结果' : '可选项' }}（{{ visibleParts.length }}）</span>
          </button>
          <button class="tool-btn" :disabled="!visibleParts.length || submitting" @click="invertSelection">{{ searchQuery || showSelectedOnly ? '反选结果' : '反选' }}</button>
          <button v-if="showSelectedOnly && selectedList.length" class="tool-btn" @click="deselectAll">清空已选</button>
        </div>
        <template #status>
          <p v-if="detail.collectionIncomplete" class="toolbar-status quality-note">合集列表未完整返回。</p>
          <p v-if="loadError && !loading" class="toolbar-status quality-note access-note">部分视频读取失败。 <button class="text-action" @click="loadGroups">重试</button></p>
        </template>
      </EpisodeSelectorToolbar>

      <!-- Episode Grid View -->
      <CollectionPartsPanel v-if="activeVideo" :key="activeVideo.bvid" :video="activeVideo" :selection="selectedMap" :access-for="accessFor" :can-select="canSelect" @back="activeVideoID = ''" @select="selectedMap = $event" />
      <div v-if="!activeVideo && showSelectedOnly" class="selected-view-heading">
        <div><strong>已选文件</strong><span>共 {{ selectedList.length }} 个文件</span></div>
        <button @click="showSelectedOnly = false">返回全部分集</button>
      </div>
      <div v-if="!activeVideo" ref="episodeGrid" class="episode-grid">
        <div
          v-for="ep in visibleEpisodes"
          :key="`${ep.index}-${ep.bvid}`"
          class="ep-card collection-card"
          :class="{ selected: selectedCount(ep) > 0 }"
          :data-access="groupAccessByVideo[ep.bvid]?.state"
          :data-cid="ep.cid"
        >
          <div
            class="collection-main"
            role="checkbox"
            :aria-checked="selectedCount(ep) === 0 ? 'false' : selectedCount(ep) === selectablePartsFor(ep).length ? 'true' : 'mixed'"
            :aria-current="ep.bvid === linkedEpisode?.bvid || undefined"
            :aria-disabled="!selectablePartsFor(ep).length"
            :aria-description="groupAccessByVideo[ep.bvid]?.reason"
            :title="groupAccessByVideo[ep.bvid]?.reason || ep.videoTitle || ep.title"
            tabindex="0"
            @click="toggleGroup(ep)"
            @keydown.space.prevent="toggleGroup(ep)"
            @keydown.enter.prevent="toggleGroup(ep)"
          >
          <EpisodeSelectionMark
            :state="groupAccessByVideo[ep.bvid]?.state === 'checking' && selectablePartsFor(ep).length ? undefined : groupAccessByVideo[ep.bvid]?.state"
            :checked="selectedCount(ep) === 0 ? false : selectedCount(ep) === selectablePartsFor(ep).length ? true : 'mixed'"
          />

          <div class="ep-content">
            <div class="ep-top">
              <span class="ep-index">第 {{ ep.index }} 集</span>
              <span v-if="ep.bvid === linkedEpisode?.bvid" class="current-badge" title="当前链接指向这一集">当前</span>
              <span v-if="ep.badge" class="badge badge-pink ep-badge">{{ ep.badge }}</span>
              <span class="ep-duration"><Clock :size="11" />{{ ep.durationStr }}</span>
            </div>
            <div class="ep-title">{{ ep.videoTitle || ep.title }}</div>
          </div>
          </div>
          <div v-if="groupErrors[ep.bvid] || partsFor(ep).length > 1" class="collection-card-footer">
            <button v-if="groupErrors[ep.bvid]" class="text-action" :title="groupErrors[ep.bvid]" :disabled="loading" @click="retryGroup(ep)">读取失败 · 重试</button>
            <button v-else class="parts-toggle" @click="openParts(ep)">{{ partsFor(ep).length }} 个分 P<span v-if="selectedCount(ep)"> · 已选 {{ selectedCount(ep) }}</span><ChevronRight :size="13" /></button>
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

<style scoped src="./episode-selector.css"></style>
<style scoped>
.quality-note { color: var(--text-secondary); font-size: 11px; }
.access-note { color: var(--warning); }
.text-action { background: transparent; color: var(--bili-pink); font-size: 11.5px; }
.collection-card { flex-direction: column; align-items: stretch; gap: 0; padding: 0; min-width: 0; cursor: default; }
.collection-main { display: flex; flex: 1; align-items: flex-start; gap: 8px; min-width: 0; padding: 10px; cursor: pointer; }
.collection-main[aria-disabled="true"] { cursor: default; }
.collection-main:focus-visible { outline: 2px solid var(--bili-pink); outline-offset: -2px; }
.collection-card:not(.selected):has(.collection-main[aria-disabled="false"]:hover) { background: var(--bg-card-hover); border-color: var(--border-hover); }
.collection-card-footer { display: flex; justify-content: flex-end; padding: 0 10px 8px; }
.parts-toggle { display: inline-flex; align-items: center; gap: 3px; padding: 3px 5px; border-radius: var(--radius-xs); background: var(--neutral-05); color: var(--text-secondary); font-size: 11px; }
.parts-toggle:hover { color: var(--bili-pink); }
.selected-link { background: transparent; color: var(--bili-pink); font-size: 12px; padding: 6px 0; }
.selected-link:hover { text-decoration: underline; }
.selected-view-heading { flex-shrink: 0; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 20px; background: var(--neutral-03); border-bottom: 1px solid var(--border-subtle); }
.selected-view-heading > div { display: flex; align-items: baseline; gap: 10px; }
.selected-view-heading strong { font-size: 12px; color: var(--text-primary); }
.selected-view-heading span { font-size: 11px; color: var(--text-secondary); }
.selected-view-heading button { border: 0; padding: 4px 0; background: transparent; color: var(--bili-pink); font-size: 11px; }
</style>
