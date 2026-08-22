<script setup lang="ts">
import { ref, watch } from 'vue'
import {
  Search,
  ClipboardPaste,
  X,
  Layers,
  Sparkles,
  ThumbsUp,
  MessageSquare,
  Eye,
  Film,
  Download,
  Check,
  Calendar
} from 'lucide-vue-next'
import { bilibili } from '../../wailsjs/go/models'
import { ParseURL, GetAvailableQualities, ReadClipboard } from '../../wailsjs/go/main/App'

const props = defineProps<{
  defaultQuality: string
  defaultCodec: string
}>()

const emit = defineEmits<{
  (e: 'open-episodes', detail: bilibili.VideoDetail, selectedQuality: string, selectedCodec: string): void
  (e: 'quick-download-single', detail: bilibili.VideoDetail, ep: bilibili.EpisodeInfo, quality: string, codec: string): void
  (e: 'show-toast', msg: string, type: 'success' | 'error' | 'info'): void
}>()

const inputUrl = ref('')
const isParsing = ref(false)
const parsedDetail = ref<bilibili.VideoDetail | null>(null)
const availableQualities = ref<bilibili.QualityOption[]>([])
const selectedQuality = ref(props.defaultQuality || 'highest')
const isFetchingQualities = ref(false)

// 监听偏好设置变更
watch(() => props.defaultQuality, (val) => {
  if (val) selectedQuality.value = val
})

function normalizeImg(url?: string) {
  if (!url) return ''
  if (url.startsWith('//')) return 'https:' + url
  if (url.startsWith('http://')) return 'https://' + url.substring(7)
  return url
}

// 一键粘贴剪贴板
async function handlePaste() {
  try {
    const text = await ReadClipboard()
    if (text) {
      inputUrl.value = text.trim()
      handleParse()
    } else {
      emit('show-toast', '剪贴板为空', 'info')
    }
  } catch (err: any) {
    emit('show-toast', '读取剪贴板失败', 'error')
  }
}

function handleClear() {
  inputUrl.value = ''
  parsedDetail.value = null
  availableQualities.value = []
}

// 执行解析
async function handleParse() {
  const url = inputUrl.value.trim()
  if (!url) {
    emit('show-toast', '请输入视频链接或 BV/AV 号', 'info')
    return
  }

  isParsing.value = true
  try {
    const res = await ParseURL(url)
    if (!res) {
      throw new Error('未返回视频信息')
    }
    parsedDetail.value = res
    emit('show-toast', '解析成功！', 'success')

    // 获取可用画质列表
    if (res.episodes && res.episodes.length > 0) {
      fetchQualities(res, res.episodes[0])
    }
  } catch (err: any) {
    emit('show-toast', err?.message || '解析失败，请检查链接是否正确', 'error')
  } finally {
    isParsing.value = false
  }
}

// 异步加载可用画质
async function fetchQualities(detail: bilibili.VideoDetail, ep: bilibili.EpisodeInfo) {
  isFetchingQualities.value = true
  try {
    const qList = await GetAvailableQualities(
      ep.bvid || detail.bvid,
      ep.aid || detail.aid,
      ep.cid,
      ep.epid || 0,
      detail.type === 'bangumi'
    )
    availableQualities.value = qList || []

    // 校验当前选中的清晰度是否不可用
    if (selectedQuality.value !== 'highest') {
      const currentOpt = qList?.find(q => q.id.toString() === selectedQuality.value)
      if (currentOpt && !currentOpt.isAvailable) {
        selectedQuality.value = 'highest'
      }
    }
  } catch (e) {
    // 允许默认 highest
  } finally {
    isFetchingQualities.value = false
  }
}

function handleSingleDownload() {
  if (!parsedDetail.value || !parsedDetail.value.episodes || parsedDetail.value.episodes.length === 0) return
  const ep = parsedDetail.value.episodes[0]
  emit('quick-download-single', parsedDetail.value, ep, selectedQuality.value, props.defaultCodec || 'auto')
}

function handleOpenEpisodes() {
  if (!parsedDetail.value) return
  emit('open-episodes', parsedDetail.value, selectedQuality.value, props.defaultCodec || 'auto')
}

// 格式化数字
function formatCount(num: number): string {
  if (!num) return '0'
  if (num >= 100000000) return (num / 100000000).toFixed(1) + '亿'
  if (num >= 10000) return (num / 10000).toFixed(1) + '万'
  return num.toString()
}

function formatPubDate(ts: number): string {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  return `${d.getFullYear()}-${(d.getMonth() + 1).toString().padStart(2, '0')}-${d.getDate().toString().padStart(2, '0')}`
}

defineExpose({
  setAndParse: (url: string) => {
    inputUrl.value = url
    handleParse()
  }
})
</script>

<template>
  <div class="parse-container">
    <!-- Hero Search Input Box -->
    <div class="input-card">
      <div class="search-wrap">
        <Search class="search-icon" :size="18" />
        <input
          v-model="inputUrl"
          type="text"
          class="url-input"
          placeholder="粘贴 B 站链接 / b23.tv 短链 / BV / AV / ep / 手机口令分享文本..."
          @keyup.enter="handleParse"
        />

        <div class="input-actions">
          <button v-if="inputUrl" class="btn-icon clear-btn" @click="handleClear" title="清空">
            <X :size="15" />
          </button>
          
          <button class="btn-secondary paste-btn" @click="handlePaste" title="从剪贴板粘贴">
            <ClipboardPaste :size="14" />
            <span>粘贴</span>
          </button>

          <button class="btn-primary parse-btn" :disabled="isParsing" @click="handleParse">
            <Sparkles v-if="!isParsing" :size="15" />
            <span v-if="!isParsing">一键解析</span>
            <span v-else class="parsing-spinner">解析中...</span>
          </button>
        </div>
      </div>
    </div>

    <!-- Video Result Card -->
    <div v-if="parsedDetail" class="result-card">
      <div class="card-left">
        <div class="cover-wrapper">
          <img
            v-if="parsedDetail.cover"
            :src="normalizeImg(parsedDetail.cover)"
            class="video-cover"
            referrerpolicy="no-referrer"
            alt="Cover"
          />
          <div v-else class="cover-fallback">
            <Film :size="32" />
          </div>
          <span class="duration-badge">{{ parsedDetail.durationStr }}</span>
          <span v-if="parsedDetail.isCollection" class="collection-badge">
            <Layers :size="11" />
            <span>合集 · {{ parsedDetail.totalParts }}P</span>
          </span>
        </div>
      </div>

      <div class="card-right">
        <div class="video-header">
          <h2 class="video-title" :title="parsedDetail.title">{{ parsedDetail.title }}</h2>
        </div>

        <div class="meta-row">
          <div class="author-box">
            <img
              v-if="parsedDetail.ownerFace"
              :src="normalizeImg(parsedDetail.ownerFace)"
              class="owner-avatar"
              referrerpolicy="no-referrer"
              alt="Avatar"
            />
            <span class="owner-name">{{ parsedDetail.ownerName }}</span>
            <span v-if="parsedDetail.pubDate" class="pub-date">
              <Calendar :size="11" />
              <span>{{ formatPubDate(parsedDetail.pubDate) }}</span>
            </span>
          </div>

          <div class="stats-box">
            <span class="stat-item" title="播放量">
              <Eye :size="12" />
              <span>{{ formatCount(parsedDetail.viewCount) }}</span>
            </span>
            <span class="stat-item" title="点赞数">
              <ThumbsUp :size="12" />
              <span>{{ formatCount(parsedDetail.likeCount) }}</span>
            </span>
            <span class="stat-item" title="弹幕数">
              <MessageSquare :size="12" />
              <span>{{ formatCount(parsedDetail.danmakuCount) }}</span>
            </span>
          </div>
        </div>

        <div class="desc-box" :title="parsedDetail.description">
          {{ parsedDetail.description || '暂无简介' }}
        </div>

        <!-- Download Action & Options Area -->
        <div class="action-footer">
          <div class="options-wrapper">
            <!-- Quality Selector (Clean & Clear with disabled/grey options for locked qualities) -->
            <div class="select-group">
              <label class="select-label">清晰度:</label>
              <select v-model="selectedQuality" class="quality-select">
                <option value="highest">最高画质 (自动选择)</option>
                <option
                  v-for="q in availableQualities"
                  :key="q.id"
                  :value="q.id.toString()"
                  :disabled="!q.isAvailable"
                  :class="{ 'opt-disabled': !q.isAvailable }"
                >
                  {{ q.label }} {{ !q.isAvailable ? (q.isVipRequired ? '[需大会员]' : (q.isLoginRequired ? '[需登录]' : '[不可用]')) : '' }}
                </option>
              </select>
            </div>
          </div>

          <!-- Actions Button -->
          <div class="action-btn-wrapper">
            <button
              v-if="parsedDetail.isCollection || parsedDetail.totalParts > 1"
              class="btn-primary main-action-btn"
              @click="handleOpenEpisodes"
            >
              <Layers :size="15" />
              <span>选择分集 (共 {{ parsedDetail.totalParts }} 集)</span>
            </button>

            <button
              v-else
              class="btn-primary main-action-btn"
              @click="handleSingleDownload"
            >
              <Download :size="15" />
              <span>立即下载</span>
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Empty / Guide State -->
    <div v-else class="empty-guide">
      <div class="guide-icon-box">
        <Film :size="32" />
      </div>
      <h3 class="guide-title">粘贴视频链接即可高速下载</h3>
      <p class="guide-desc">
        支持普通视频、番剧动漫、多P合集、电影纪录片以及短链接（b23.tv）
      </p>
      <div class="features-row">
        <div class="feature-item">
          <Check :size="14" class="check-icon" />
          <span>8K / 4K / 1080P60 极清画质</span>
        </div>
        <div class="feature-item">
          <Check :size="14" class="check-icon" />
          <span>杜比全景声 / Hi-Res 无损音质</span>
        </div>
        <div class="feature-item">
          <Check :size="14" class="check-icon" />
          <span>多P批量与合集自由选集</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.parse-container {
  display: flex;
  flex-direction: column;
  gap: 18px;
  width: 100%;
  max-width: 920px;
  margin: 0 auto;
}

.input-card {
  border-radius: var(--radius-md);
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  padding: 4px 6px;
  box-shadow: var(--shadow-sm);
  transition: border-color var(--transition-fast);
}

.input-card:focus-within {
  border-color: var(--border-hover);
}

.search-wrap {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 2px 6px;
}

.search-icon {
  color: var(--bili-pink);
  margin-left: 4px;
  flex-shrink: 0;
}

.url-input {
  flex: 1;
  height: 38px;
  background: transparent;
  border: none;
  font-size: 13.5px;
  color: #fff;
}

.url-input:focus {
  box-shadow: none;
  border: none;
}

.input-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

.clear-btn {
  background: rgba(255, 255, 255, 0.05);
}

.paste-btn {
  height: 34px;
  padding: 0 12px;
}

.parse-btn {
  height: 34px;
  padding: 0 16px;
  font-size: 13px;
}

.result-card {
  display: flex;
  gap: 18px;
  padding: 16px;
  border-radius: var(--radius-md);
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  box-shadow: var(--shadow-md);
}

.card-left {
  width: 240px;
  min-width: 240px;
}

.cover-wrapper {
  position: relative;
  width: 100%;
  aspect-ratio: 16 / 10;
  border-radius: var(--radius-sm);
  overflow: hidden;
  background: rgba(0, 0, 0, 0.3);
}

.video-cover {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.cover-fallback {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
}

.duration-badge {
  position: absolute;
  bottom: 6px;
  right: 6px;
  background: rgba(0, 0, 0, 0.75);
  backdrop-filter: blur(4px);
  color: #fff;
  font-size: 10.5px;
  font-weight: 600;
  padding: 1px 5px;
  border-radius: var(--radius-xs);
}

.collection-badge {
  position: absolute;
  top: 6px;
  left: 6px;
  background: var(--bili-pink);
  color: #fff;
  font-size: 10.5px;
  font-weight: 600;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  display: flex;
  align-items: center;
  gap: 3px;
}

.card-right {
  flex: 1;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  min-width: 0;
}

.video-title {
  font-size: 15.5px;
  font-weight: 700;
  color: #fff;
  line-height: 1.4;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.meta-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin: 8px 0;
  flex-wrap: wrap;
  gap: 8px;
}

.author-box {
  display: flex;
  align-items: center;
  gap: 8px;
}

.owner-avatar {
  width: 22px;
  height: 22px;
  border-radius: var(--radius-full);
  object-fit: cover;
}

.owner-name {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--text-secondary);
}

.pub-date {
  display: flex;
  align-items: center;
  gap: 3px;
  font-size: 11px;
  color: var(--text-muted);
}

.stats-box {
  display: flex;
  align-items: center;
  gap: 12px;
}

.stat-item {
  display: flex;
  align-items: center;
  gap: 4px;
  color: var(--text-muted);
  font-size: 11.5px;
}

.desc-box {
  font-size: 12px;
  color: var(--text-muted);
  line-height: 1.4;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  margin-bottom: 12px;
}

.action-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border-subtle);
}

.options-wrapper {
  display: flex;
  align-items: center;
  gap: 12px;
}

.select-group {
  display: flex;
  align-items: center;
  gap: 6px;
}

.select-label {
  font-size: 12px;
  color: var(--text-secondary);
  white-space: nowrap;
}

.quality-select {
  height: 34px;
  padding: 0 10px;
  font-size: 12.5px;
  min-width: 210px;
}

.quality-select option:disabled {
  color: #555e6d;
  background: #141720;
}

.action-btn-wrapper {
  margin-left: auto;
  flex-shrink: 0;
}

.main-action-btn {
  height: 34px;
  padding: 0 18px;
  font-size: 13px;
  white-space: nowrap;
}

/* Empty State */
.empty-guide {
  margin-top: 24px;
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  padding: 36px 20px;
}

.guide-icon-box {
  width: 60px;
  height: 60px;
  border-radius: var(--radius-md);
  background: rgba(251, 114, 153, 0.12);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--bili-pink);
  margin-bottom: 14px;
}

.guide-title {
  font-size: 16px;
  font-weight: 700;
  color: var(--text-primary);
  margin-bottom: 6px;
}

.guide-desc {
  font-size: 13px;
  color: var(--text-muted);
  max-width: 440px;
  margin-bottom: 20px;
}

.features-row {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  justify-content: center;
}

.feature-item {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--text-secondary);
}

.check-icon {
  color: var(--success);
}
</style>
