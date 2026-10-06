<script setup lang="ts">
import { ref, computed, onMounted, nextTick, watch } from 'vue'
import {
  Layers,
  X,
  CheckSquare,
  Square,
  Clock,
  Download,
  Search
} from 'lucide-vue-next'
import { bilibili } from '../../wailsjs/go/models'
import { GetPlaybackInfo } from '../../wailsjs/go/main/App'
import { filterEpisodes, selectEpisodes } from '../utils/episode-selection'

const props = defineProps<{
  detail: bilibili.VideoDetail
  initialQuality: string
  initialCodec: string
  userInfo?: bilibili.UserInfo | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', cids: number[], quality: string, codec: string): void
}>()

const targetQuality = ref(props.initialQuality || 'highest')
const targetCodec = ref(props.initialCodec || 'auto')
const availableQualities = ref<bilibili.QualityOption[]>([])
const selectedMap = ref<Record<number, boolean>>({})
const rangeInput = ref('')
const searchQuery = ref('')
const showSelectedOnly = ref(false)
const showAdvanced = ref(false)
const episodeGrid = ref<HTMLElement | null>(null)
const playbackError = ref<bilibili.PlaybackError | null>(null)
const linkedEpisode = computed(() => props.detail.hasLinkedEpisode ? props.detail.episodes[(props.detail.defaultPage || 1) - 1] : undefined)
const filteredEpisodes = computed(() => filterEpisodes(props.detail.episodes, searchQuery.value))
const visibleEpisodes = computed(() => showSelectedOnly.value ? selectedList.value : filteredEpisodes.value)
const hiddenSelectedCount = computed(() => {
  const visibleCids = new Set(visibleEpisodes.value.map(ep => ep.cid))
  return selectedList.value.filter(ep => !visibleCids.has(ep.cid)).length
})
const selectionScope = computed(() => searchQuery.value.trim() || showSelectedOnly.value ? '结果' : '全部')
watch(searchQuery, () => { showSelectedOnly.value = false })
watch(() => props.userInfo, loadQualities)

async function locateCurrentEpisode() {
  searchQuery.value = ''
  showSelectedOnly.value = false
  await nextTick()
  const card = episodeGrid.value?.querySelector<HTMLElement>(`[data-cid="${linkedEpisode.value?.cid}"]`)
  card?.scrollIntoView({ block: 'nearest' })
}

onMounted(async () => {
  // 默认全部不勾选，由用户按需勾选
  selectedMap.value = {}
  await locateCurrentEpisode()
  await loadQualities()
})

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
  return props.detail.episodes.filter(ep => selectedMap.value[ep.cid])
})

function toggleEpisode(cid: number) {
  selectedMap.value[cid] = !selectedMap.value[cid]
}

function selectAll() {
  selectedMap.value = selectEpisodes(selectedMap.value, visibleEpisodes.value)
}

function deselectAll() {
  selectedMap.value = {}
}

function invertSelection() {
  selectedMap.value = selectEpisodes(selectedMap.value, visibleEpisodes.value, true)
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
            map[ep.cid] = true
          }
        })
      }
    } else {
      const num = parseInt(part, 10)
      if (!isNaN(num)) {
        props.detail.episodes.forEach(ep => {
          if (ep.index === num) {
            map[ep.cid] = true
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
  if (cids.length === 0) return
  emit('submit', cids, targetQuality.value, targetCodec.value)
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
      <div class="toolbar">
        <div class="search-row">
          <label class="episode-search">
            <Search :size="15" />
            <input v-model="searchQuery" type="search" placeholder="搜索分集标题或集号，如 美学、P8" aria-label="搜索分集标题或集号" />
            <button v-if="searchQuery" class="search-clear" @click="searchQuery = ''" aria-label="清除搜索"><X :size="14" /></button>
          </label>
          <span class="search-count">{{ showSelectedOnly ? '已选' : '显示' }} {{ visibleEpisodes.length }} / {{ detail.totalParts }} {{ detail.type === 'cheese' ? '课时' : '集' }}</span>
        </div>
        <div v-if="linkedEpisode" class="current-link-row">
          <span :title="linkedEpisode.title">当前链接：{{ detail.type === 'cheese' ? '课' : 'P' }}{{ linkedEpisode.index }} · {{ linkedEpisode.title }}</span>
          <button class="text-action" @click="locateCurrentEpisode">定位这一{{ detail.type === 'cheese' ? '课' : '集' }}</button>
        </div>
        <div class="selection-row">
        <div class="tool-actions">
          <button class="tool-btn" :disabled="visibleEpisodes.length === 0" @click="selectAll">
            <CheckSquare :size="14" />
            <span>全选{{ selectionScope }}（{{ visibleEpisodes.length }}）</span>
          </button>
          <button class="tool-btn" :disabled="visibleEpisodes.length === 0" @click="invertSelection">
            <span>反选{{ selectionScope }}</span>
          </button>
          <button class="tool-btn" @click="deselectAll">
            <span>清空已选</span>
          </button>

          <button class="tool-btn" :class="{ active: showSelectedOnly }" @click="showSelectedOnly = !showSelectedOnly">{{ showSelectedOnly ? '返回列表' : `查看已选（${selectedList.length}）` }}</button>
        </div>

        <div class="tool-options">
          <div class="opt-item">
            <span class="opt-label">画质:</span>
            <select v-model="targetQuality" class="opt-select">
              <option value="highest">最高画质 (自动选择)</option>
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

          <button class="text-action" @click="showAdvanced = !showAdvanced">{{ showAdvanced ? '收起选项' : '更多选项' }}</button>
        </div>
        </div>
        <div v-if="showAdvanced" class="advanced-options">
          <div class="range-box"><span class="opt-label">按集号选择:</span><input v-model="rangeInput" class="range-input" placeholder="如 1-10 或 1,3,5" @keyup.enter="applyRangeSelection" /><button class="btn-secondary range-btn" @click="applyRangeSelection">添加选择</button></div>
          <div class="opt-item"><span class="opt-label">编码:</span><select v-model="targetCodec" class="opt-select"><option value="auto">自动选择（同画质优先兼容）</option><option value="AVC">AVC / H.264（兼容性最好）</option><option value="HEVC">HEVC / H.265（高压缩比）</option><option value="AV1">AV1（需播放器支持）</option></select></div>
        </div>
        <p v-if="playbackError" class="quality-note access-note">{{ linkedEpisode ? '当前链接' : '首集' }}：{{ playbackError.message }}。其他分集按各自观看权限检查。</p>
        <p v-else class="quality-note">画质选项参考{{ linkedEpisode ? '当前链接' : '首集' }}；“最高画质”按每集可用画质分别选择。</p>
      </div>

      <!-- Episode Grid View -->
      <div ref="episodeGrid" class="episode-grid">
        <div
          v-for="ep in visibleEpisodes"
          :key="ep.cid"
          class="ep-card"
          :class="{ selected: selectedMap[ep.cid], 'current-episode': ep.cid === linkedEpisode?.cid }"
          :data-cid="ep.cid"
          role="checkbox"
          :aria-checked="!!selectedMap[ep.cid]"
          :aria-label="`P${ep.index} ${ep.title}`"
          tabindex="0"
          @click="toggleEpisode(ep.cid)"
          @keydown.space.prevent="toggleEpisode(ep.cid)"
          @keydown.enter.prevent="toggleEpisode(ep.cid)"
        >
          <div class="ep-check">
            <CheckSquare v-if="selectedMap[ep.cid]" :size="16" class="checked-icon" />
            <Square v-else :size="16" class="unchecked-icon" />
          </div>

          <div class="ep-content">
            <div class="ep-top">
              <span class="ep-index">{{ detail.type === 'cheese' ? '课' : 'P' }}{{ ep.index }}</span>
              <span v-if="ep.cid === linkedEpisode?.cid" class="current-badge">当前链接</span>
              <span v-if="ep.badge" class="badge badge-pink ep-badge">{{ ep.badge }}</span>
              <span class="ep-duration">
                <Clock :size="11" />
                <span>{{ ep.durationStr }}</span>
              </span>
            </div>
            <div class="ep-title" :title="ep.title">{{ ep.title }}</div>
          </div>
        </div>
        <div v-if="visibleEpisodes.length === 0" class="no-episodes"><Search :size="24" /><p>{{ showSelectedOnly ? '还没有选择分集' : '没有找到匹配的分集' }}</p><button v-if="searchQuery || showSelectedOnly" class="text-action" @click="searchQuery = ''; showSelectedOnly = false">显示全部分集</button></div>
      </div>

      <!-- Modal Footer -->
      <div class="modal-footer">
        <div class="footer-stats">
          已选择 <span class="highlight-num">{{ selectedList.length }}</span> / {{ detail.totalParts }} {{ detail.type === 'cheese' ? '课时' : '集' }}
          <span v-if="hiddenSelectedCount" class="hidden-selected">其中 {{ hiddenSelectedCount }} {{ detail.type === 'cheese' ? '课时' : '集' }}不在当前结果中 <button class="text-action" @click="showSelectedOnly = true">查看已选</button></span>
        </div>

        <div class="footer-actions">
          <button class="btn-secondary cancel-btn" @click="emit('close')">取消</button>
          <button
            class="btn-primary start-btn"
            :disabled="selectedList.length === 0"
            @click="handleSubmit"
          >
            <Download :size="15" />
            <span>开始下载 (已选 {{ selectedList.length }} {{ detail.type === 'cheese' ? '课时' : '集' }})</span>
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
.advanced-options { display: flex; width: 100%; flex-wrap: wrap; align-items: center; gap: 12px 20px; }
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
</style>
