<script setup lang="ts">
import { ref, computed, onMounted, nextTick, watch } from 'vue'
import {
  Layers,
  X,
  CheckSquare,
  Clock,
  Download,
  Search
} from 'lucide-vue-next'
import { bilibili } from '../../wailsjs/go/models'
import { filterEpisodes, selectEpisodes } from '../utils/episode-selection'
import { useEpisodePlayback } from '../utils/episode-playback'
import EpisodeSelectionMark from './EpisodeSelectionMark.vue'
import EpisodeSelectorToolbar from './EpisodeSelectorToolbar.vue'

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
const selectedMap = ref<Record<number, boolean>>({})
const searchQuery = ref('')
const showSelectedOnly = ref(false)
const episodeGrid = ref<HTMLElement | null>(null)
const linkedEpisode = computed(() => props.detail.hasLinkedEpisode ? props.detail.episodes[(props.detail.defaultPage || 1) - 1] : undefined)
const { referenceQualities: availableQualities, accessFor, canSelect } = useEpisodePlayback(
  () => props.detail, () => props.userInfo, () => props.detail.episodes
)
const filteredEpisodes = computed(() => filterEpisodes(props.detail.episodes, searchQuery.value))
const visibleEpisodes = computed(() => showSelectedOnly.value ? selectedList.value : filteredEpisodes.value)
const selectableEpisodes = computed(() => props.detail.episodes.filter(ep => canSelect(ep.cid)))
const selectableVisible = computed(() => visibleEpisodes.value.filter(ep => canSelect(ep.cid)))
const hiddenSelectedCount = computed(() => {
  const visibleCids = new Set(visibleEpisodes.value.map(ep => ep.cid))
  return selectedList.value.filter(ep => !visibleCids.has(ep.cid)).length
})
const selectionScope = computed(() => searchQuery.value.trim() || showSelectedOnly.value ? '结果' : '全部')
watch(searchQuery, () => { showSelectedOnly.value = false })
watch(availableQualities, qualities => {
  if (qualities.some(q => String(q.id) === targetQuality.value && !q.isAvailable)) targetQuality.value = 'highest'
})

async function locateCurrentEpisode() {
  searchQuery.value = ''
  showSelectedOnly.value = false
  await nextTick()
  const card = episodeGrid.value?.querySelector<HTMLElement>(`[data-cid="${linkedEpisode.value?.cid}"]`)
  card?.scrollIntoView({ block: 'nearest' })
}

onMounted(locateCurrentEpisode)

const selectedList = computed(() => {
  return selectableEpisodes.value.filter(ep => selectedMap.value[ep.cid])
})

function toggleEpisode(cid: number) {
  if (!canSelect(cid)) return
  selectedMap.value[cid] = !selectedMap.value[cid]
}

function selectAll() {
  selectedMap.value = selectEpisodes(selectedMap.value, selectableVisible.value)
}

function deselectAll() {
  selectedMap.value = {}
}

function invertSelection() {
  selectedMap.value = selectEpisodes(selectedMap.value, selectableVisible.value, true)
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
          <h2 class="title-text" :title="detail.collectionTitle || detail.title">{{ detail.collectionTitle || detail.title }}</h2>
        </div>
        <button class="btn-icon close-btn" @click="emit('close')" title="关闭">
          <X :size="18" />
        </button>
      </div>

      <EpisodeSelectorToolbar
        v-model:search="searchQuery"
        v-model:quality="targetQuality"
        v-model:codec="targetCodec"
        :qualities="availableQualities"
        :can-locate="!!linkedEpisode"
        @locate="locateCurrentEpisode"
      >
        <div class="tool-actions">
          <button class="tool-btn" :disabled="!selectableVisible.length" @click="selectAll">
            <CheckSquare :size="14" />
            <span>全选{{ selectionScope === '结果' ? '结果' : '可选项' }}（{{ selectableVisible.length }}）</span>
          </button>
          <button class="tool-btn" :disabled="!selectableVisible.length" @click="invertSelection">{{ selectionScope === '结果' ? '反选结果' : '反选' }}</button>
          <button v-if="selectedList.length" class="tool-btn" @click="deselectAll">清空已选</button>
          <button v-if="selectedList.length || showSelectedOnly" class="tool-btn" :class="{ active: showSelectedOnly }" @click="showSelectedOnly = !showSelectedOnly">{{ showSelectedOnly ? '返回列表' : '查看已选' }}</button>
        </div>
      </EpisodeSelectorToolbar>

      <!-- Episode Grid View -->
      <div ref="episodeGrid" class="episode-grid">
        <div
          v-for="ep in visibleEpisodes"
          :key="ep.cid"
          class="ep-card"
          :class="{ selected: canSelect(ep.cid) && selectedMap[ep.cid] }"
          :data-access="accessFor(ep.cid).state"
          :data-cid="ep.cid"
          role="checkbox"
          :aria-checked="canSelect(ep.cid) && !!selectedMap[ep.cid]"
          :aria-current="ep.cid === linkedEpisode?.cid || undefined"
          :aria-disabled="!canSelect(ep.cid)"
          :aria-description="accessFor(ep.cid).reason"
          :title="accessFor(ep.cid).reason || ep.title"
          tabindex="0"
          @click="toggleEpisode(ep.cid)"
          @keydown.space.prevent="toggleEpisode(ep.cid)"
          @keydown.enter.prevent="toggleEpisode(ep.cid)"
        >
          <EpisodeSelectionMark :state="accessFor(ep.cid).state" :checked="canSelect(ep.cid) && !!selectedMap[ep.cid]" />

          <div class="ep-content">
            <div class="ep-top">
              <span class="ep-index">{{ detail.type === 'cheese' ? '课' : 'P' }}{{ ep.index }}</span>
              <span v-if="ep.cid === linkedEpisode?.cid" class="current-badge" title="当前链接指向这一集">当前</span>
              <span v-if="ep.badge" class="badge badge-pink ep-badge">{{ ep.badge }}</span>
              <span class="ep-duration">
                <Clock :size="11" />
                <span>{{ ep.durationStr }}</span>
              </span>
            </div>
            <div class="ep-title">{{ ep.title }}</div>
          </div>
        </div>
        <div v-if="visibleEpisodes.length === 0" class="no-episodes"><Search :size="24" /><p>{{ showSelectedOnly ? '还没有选择分集' : '没有找到匹配的分集' }}</p><button v-if="searchQuery || showSelectedOnly" class="text-action" @click="searchQuery = ''; showSelectedOnly = false">显示全部分集</button></div>
      </div>

      <!-- Modal Footer -->
      <div class="modal-footer">
        <div class="footer-stats">
          已选 <span class="highlight-num">{{ selectedList.length }}</span> {{ detail.type === 'cheese' ? '课时' : '集' }}
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
            <span>开始下载</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped src="./episode-selector.css"></style>
<style scoped>
.text-action { color: var(--bili-pink); font-size: 11.5px; flex-shrink: 0; background: transparent; }
.hidden-selected { display: block; margin-top: 4px; color: var(--warning); font-size: 11px; }
.ep-card[aria-disabled="false"]:not(.selected):hover { background: var(--bg-card-hover); border-color: var(--border-hover); }
</style>
