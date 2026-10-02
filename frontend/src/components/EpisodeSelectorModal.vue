<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  Layers,
  X,
  CheckSquare,
  Square,
  Clock,
  Download
} from 'lucide-vue-next'
import { bilibili } from '../../wailsjs/go/models'
import { GetAvailableQualities } from '../../wailsjs/go/main/App'

const props = defineProps<{
  detail: bilibili.VideoDetail
  initialQuality: string
  initialCodec: string
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

onMounted(async () => {
  // 默认全部不勾选，由用户按需勾选
  selectedMap.value = {}

  // 加载画质列表
  if (props.detail.episodes && props.detail.episodes.length > 0) {
    try {
      const ep0 = props.detail.episodes[0]
      const qList = await GetAvailableQualities(
        ep0.bvid || props.detail.bvid,
        ep0.aid || props.detail.aid,
        ep0.cid,
        ep0.epid || 0,
        props.detail.type === 'bangumi'
      )
      if (qList) {
        availableQualities.value = qList
        if (targetQuality.value !== 'highest') {
          const currentOpt = qList.find(q => q.id.toString() === targetQuality.value)
          if (currentOpt && !currentOpt.isAvailable) {
            targetQuality.value = 'highest'
          }
        }
      }
    } catch (e) {}
  }
})

const selectedList = computed(() => {
  return props.detail.episodes.filter(ep => selectedMap.value[ep.cid])
})

function toggleEpisode(cid: number) {
  selectedMap.value[cid] = !selectedMap.value[cid]
}

function selectAll() {
  const map: Record<number, boolean> = {}
  props.detail.episodes.forEach(ep => {
    map[ep.cid] = true
  })
  selectedMap.value = map
}

function deselectAll() {
  selectedMap.value = {}
}

function invertSelection() {
  const map: Record<number, boolean> = {}
  props.detail.episodes.forEach(ep => {
    map[ep.cid] = !selectedMap.value[ep.cid]
  })
  selectedMap.value = map
}

// 区间快速选择，如 "1-10" 或 "1,3,5-8"
function applyRangeSelection() {
  const raw = rangeInput.value.trim()
  if (!raw) return

  const map: Record<number, boolean> = {}
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
            <h2 class="title-text">分集选择与批量下载</h2>
            <span class="sub-text">{{ detail.title }} (共 {{ detail.totalParts }} 集)</span>
          </div>
        </div>
        <button class="btn-icon close-btn" @click="emit('close')" title="关闭">
          <X :size="18" />
        </button>
      </div>

      <!-- Selection & Options Toolbar -->
      <div class="toolbar">
        <div class="tool-actions">
          <button class="tool-btn" @click="selectAll">
            <CheckSquare :size="14" />
            <span>全选</span>
          </button>
          <button class="tool-btn" @click="invertSelection">
            <span>反选</span>
          </button>
          <button class="tool-btn" @click="deselectAll">
            <span>清空</span>
          </button>

          <!-- Range Quick Select -->
          <div class="range-box">
            <input
              v-model="rangeInput"
              type="text"
              class="range-input"
              placeholder="如 1-10 或 1,3,5"
              @keyup.enter="applyRangeSelection"
            />
            <button class="btn-secondary range-btn" @click="applyRangeSelection">选择</button>
          </div>
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

          <div class="opt-item">
            <span class="opt-label">编码:</span>
            <select v-model="targetCodec" class="opt-select">
              <option value="auto">智能优选 (自动推荐)</option>
              <option value="AVC">AVC / H.264 (兼容性最好)</option>
              <option value="HEVC">HEVC / H.265 (高压缩比)</option>
              <option value="AV1">AV1 (极速高画质)</option>
            </select>
          </div>
        </div>
      </div>

      <!-- Episode Grid View -->
      <div class="episode-grid">
        <div
          v-for="ep in detail.episodes"
          :key="ep.cid"
          class="ep-card"
          :class="{ selected: selectedMap[ep.cid] }"
          @click="toggleEpisode(ep.cid)"
        >
          <div class="ep-check">
            <CheckSquare v-if="selectedMap[ep.cid]" :size="16" class="checked-icon" />
            <Square v-else :size="16" class="unchecked-icon" />
          </div>

          <div class="ep-content">
            <div class="ep-top">
              <span class="ep-index">P{{ ep.index }}</span>
              <span v-if="ep.badge" class="badge badge-pink ep-badge">{{ ep.badge }}</span>
              <span class="ep-duration">
                <Clock :size="11" />
                <span>{{ ep.durationStr }}</span>
              </span>
            </div>
            <div class="ep-title" :title="ep.title">{{ ep.title }}</div>
          </div>
        </div>
      </div>

      <!-- Modal Footer -->
      <div class="modal-footer">
        <div class="footer-stats">
          已选择 <span class="highlight-num">{{ selectedList.length }}</span> / {{ detail.totalParts }} 集
        </div>

        <div class="footer-actions">
          <button class="btn-secondary cancel-btn" @click="emit('close')">取消</button>
          <button
            class="btn-primary start-btn"
            :disabled="selectedList.length === 0"
            @click="handleSubmit"
          >
            <Download :size="15" />
            <span>开始下载 (已选 {{ selectedList.length }} 集)</span>
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
  max-height: 85vh;
  display: flex;
  flex-direction: column;
}

.modal-header {
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
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 20px;
  background: var(--neutral-02);
  border-bottom: 1px solid var(--border-subtle);
  flex-wrap: wrap;
  gap: 10px;
}

.tool-actions {
  display: flex;
  align-items: center;
  gap: 6px;
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
  overflow-y: auto;
  padding: 16px 20px;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 10px;
  max-height: 420px;
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
