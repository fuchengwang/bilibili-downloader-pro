<script setup lang="ts">
import { ref, computed, watch, toRaw } from 'vue'
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
  Calendar,
  AlertCircle,
  UserRound
} from 'lucide-vue-next'
import AppSelect from './AppSelect.vue'
import { bilibili } from '../../wailsjs/go/models'
import { ParseURL, GetPlaybackInfo, ReadClipboard } from '../../wailsjs/go/main/App'
import { BrowserOpenURL } from '../../wailsjs/runtime/runtime'
import { episodeURL, isPermissionError } from '../utils/episode-selection'

const props = defineProps<{
  defaultQuality: string
  defaultCodec: string
  userInfo?: bilibili.UserInfo | null
  userInfoLoading?: boolean
  userInfoError?: boolean
}>()

const emit = defineEmits<{
  (e: 'open-episodes', detail: bilibili.VideoDetail, selectedQuality: string, selectedCodec: string, linkVersion: number): void
  (e: 'quick-download-single', detail: bilibili.VideoDetail, ep: bilibili.EpisodeInfo, quality: string, codec: string): void
  (e: 'show-toast', msg: string, type: 'success' | 'error' | 'info'): void
  (e: 'open-login'): void
}>()

const inputUrl = ref('')
const isParsing = ref(false)
const parsedDetail = ref<bilibili.VideoDetail | null>(null)
const availableQualities = ref<bilibili.QualityOption[]>([])
const selectedQuality = ref(props.defaultQuality || 'highest')
const isFetchingQualities = ref(false)
const playbackError = ref<bilibili.PlaybackError | null>(null)
let qualityRequest = 0
let parseRequest = 0
let lastParsedLink = ''
let linkVersion = 0
function currentEpisode(detail: bilibili.VideoDetail) {
  if (detail.hasLinkedEpisode === false) return undefined
  return detail.episodes[Math.max(0, (detail.defaultPage || 1) - 1)]
}
function episodeForPlaybackCheck(detail: bilibili.VideoDetail) {
  if (detail.type === 'cheese' && detail.hasLinkedEpisode === false) {
    // A free lesson cannot confirm access to the course's paid lessons.
    return detail.episodes.find(ep => ep.badge === '付费' || ep.badge === '已购买')
  }
  if (detail.type === 'bangumi' && detail.hasLinkedEpisode === false) {
    return detail.episodes.find(ep => ep.epid > 0 && ep.cid > 0 &&
      (ep.badge === '会员' || ep.badge === '大会员' || ep.badge === '付费'))
  }
  return currentEpisode(detail)
}
const linkedEpisode = computed(() => parsedDetail.value ? currentEpisode(parsedDetail.value) : undefined)
const displayTitle = computed(() => linkedEpisode.value && (parsedDetail.value?.isCollection || parsedDetail.value?.type !== 'normal') ? linkedEpisode.value.title : parsedDetail.value?.title)
const displayCover = computed(() => linkedEpisode.value?.cover || parsedDetail.value?.cover)
const displayDuration = computed(() => linkedEpisode.value?.durationStr || parsedDetail.value?.durationStr)
const singleDownloadBlocked = computed(() => isParsing.value || isFetchingQualities.value || isPermissionError(playbackError.value?.kind))
const sourceURL = computed(() => linkedEpisode.value && parsedDetail.value ? episodeURL(linkedEpisode.value, parsedDetail.value.type) : '')
const isGuest = computed(() => !props.userInfo?.isLogin && !props.userInfoLoading && !props.userInfoError)
const accountRestricted = computed(() =>
  isPermissionError(playbackError.value?.kind) && playbackError.value?.kind !== 'region_restricted'
)
const inlinePlaybackError = computed(() => {
  const error = playbackError.value
  if (accountRestricted.value && !props.userInfo?.isLogin) return null
  // An inconclusive sample does not describe access to the whole course or season.
  if (parsedDetail.value?.hasLinkedEpisode === false && !isPermissionError(error?.kind)) return null
  return error
})
const guestLoginDescription = computed(() => {
  if (parsedDetail.value?.type === 'cheese') return '请用购买课程的账号登录。'
  if (accountRestricted.value) return '请登录有完整观看权限的 B 站账号。'
  return '已购课程或充电视频请用对应账号登录，公开视频可直接下载。'
})
const automaticQualityLabel = computed(() => {
  if (isFetchingQualities.value) return '正在检查可用画质…'
  const best = availableQualities.value.reduce<bilibili.QualityOption | undefined>(
    (highest, option) => option.isAvailable && (!highest || option.id > highest.id) ? option : highest,
    undefined
  )
  if (!best) return '自动'
  const label = best.label.match(/\b(?:\d{3,4}P|[248]K)\b/i)?.[0].toUpperCase() || best.label
  return `自动（${label}）`
})

// 监听偏好设置变更
watch(() => props.defaultQuality, (val) => {
  if (val) selectedQuality.value = val
})

// 监听登录状态变更，自动刷新当前已解析视频的清晰度权限
watch(() => props.userInfo, refreshQualities)

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
  parseRequest++
  lastParsedLink = ''
  isParsing.value = false
  qualityRequest++
  isFetchingQualities.value = false
  inputUrl.value = ''
  parsedDetail.value = null
  availableQualities.value = []
  playbackError.value = null
}

// 执行解析
async function handleParse() {
  const url = inputUrl.value.trim()
  if (!url) {
    emit('show-toast', '请输入视频链接或 BV/AV 号', 'info')
    return
  }

  isParsing.value = true
  const request = ++parseRequest
  if (lastParsedLink !== url) {
    lastParsedLink = url
    linkVersion++
    selectedQuality.value = props.defaultQuality || 'highest'
  }
  qualityRequest++
  parsedDetail.value = null
  isFetchingQualities.value = false
  playbackError.value = null
  availableQualities.value = []
  try {
    const res = await ParseURL(url)
    if (request !== parseRequest) return
    if (!res) {
      throw new Error('未返回视频信息')
    }
    parsedDetail.value = res
    emit('show-toast', '解析成功！', 'success')

    refreshQualities()
  } catch (err: any) {
    if (request === parseRequest) emit('show-toast', err?.message || '解析失败，请检查链接是否正确', 'error')
  } finally {
    if (request === parseRequest) isParsing.value = false
  }
}

// 异步加载可用画质
async function fetchQualities(detail: bilibili.VideoDetail, ep: bilibili.EpisodeInfo) {
  const request = ++qualityRequest
  availableQualities.value = []
  playbackError.value = null
  isFetchingQualities.value = true
  try {
    const info = await GetPlaybackInfo(
      ep.bvid || detail.bvid,
      ep.aid || detail.aid,
      ep.cid,
      ep.epid || 0,
      detail.type === 'bangumi',
      detail.type === 'cheese'
    )
    // ref wraps details in a reactive proxy; compare the underlying objects so
    // the response to the initial parse is not mistaken for a stale request.
    if (request !== qualityRequest || toRaw(parsedDetail.value) !== toRaw(detail)) return
    playbackError.value = info?.error || null
    if (detail.hasLinkedEpisode !== false) availableQualities.value = info?.qualities || []
  } catch (err: any) {
    if (request === qualityRequest && toRaw(parsedDetail.value) === toRaw(detail)) {
      availableQualities.value = []
      if (detail.hasLinkedEpisode === false) return
      const message = String(err?.message || err || '暂时无法检查播放权限')
      playbackError.value = { kind: 'unavailable', message, hint: '请稍后重新检查。' }
      emit('show-toast', message, 'error')
    }
  } finally {
    if (request === qualityRequest) isFetchingQualities.value = false
  }
}

function handleSingleDownload() {
  if (!parsedDetail.value || singleDownloadBlocked.value) return
  const ep = currentEpisode(parsedDetail.value)
  if (!ep) return
  emit('quick-download-single', parsedDetail.value, ep, String(displayedQuality.value), props.defaultCodec || 'auto')
}

const displayedQuality = computed<string | number>({
  get: () => availableQualities.value.some(q => q.isAvailable && String(q.id) === selectedQuality.value)
    ? selectedQuality.value
    : 'highest',
  set: value => { selectedQuality.value = String(value) }
})

const qualitySelectOptions = computed(() => [
  { value: 'highest', label: automaticQualityLabel.value },
  ...availableQualities.value.map(q => ({
    value: q.id.toString(),
    label: q.label,
    badge: !q.isAvailable ? (q.isVipRequired ? '需大会员' : (q.isLoginRequired ? '需登录' : '不可用')) : undefined,
    disabled: !q.isAvailable
  }))
])

function handleOpenEpisodes() {
  if (!parsedDetail.value) return
  emit('open-episodes', parsedDetail.value, selectedQuality.value, props.defaultCodec || 'auto', linkVersion)
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

function refreshQualities() {
  if (parsedDetail.value && parsedDetail.value.episodes && parsedDetail.value.episodes.length > 0) {
    const ep = episodeForPlaybackCheck(parsedDetail.value)
    if (ep) fetchQualities(parsedDetail.value, ep)
  }
}

defineExpose({
  setAndParse: (url: string) => {
    inputUrl.value = url
    handleParse()
  }
})
</script>

<template>
  <div class="parse-container" :class="{ 'is-empty': !parsedDetail }">
    <!-- Hero Heading (Empty State) -->
    <div v-if="!parsedDetail" class="hero-head">
      <div class="hero-icon-badge">
        <Film :size="22" />
      </div>
      <h2 class="hero-title">粘贴链接，即刻解析</h2>
      <p class="hero-subtitle">支持 B 站视频、番剧、合集、课堂与 b23.tv 短链</p>
    </div>

    <!-- Hero Search Input Box -->
    <div class="input-card">
      <div class="search-wrap">
        <Search class="search-icon" :size="18" />
        <input
          v-model="inputUrl"
          type="text"
          class="url-input"
          placeholder="粘贴 B 站视频链接、BV 号或分享文本…"
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
            v-if="displayCover"
            :src="normalizeImg(displayCover)"
            class="video-cover"
            referrerpolicy="no-referrer"
            alt="Cover"
          />
          <div v-else class="cover-fallback">
            <Film :size="32" />
          </div>
          <span v-if="displayDuration" class="duration-badge">{{ displayDuration }}</span>
          <span v-if="parsedDetail.isCollection" class="collection-badge">
            <Layers :size="11" />
            <span>{{ parsedDetail.type === 'cheese' ? '课堂' : '合集' }} · {{ parsedDetail.totalParts }}{{ parsedDetail.type === 'cheese' ? '课时' : 'P' }}</span>
          </span>
        </div>
      </div>

      <div class="card-right">
        <div class="video-header">
          <h2 class="video-title" :title="displayTitle">{{ displayTitle }}</h2>
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

          <div v-if="parsedDetail.type !== 'cheese'" class="stats-box">
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

        <div v-if="inlinePlaybackError" class="playback-notice">
          <div class="notice-message" role="status">
            <AlertCircle :size="14" aria-hidden="true" />
            <span>{{ parsedDetail.type === 'bangumi' && !linkedEpisode ? '受限分集：' : '' }}{{ inlinePlaybackError.message }}</span>
          </div>
          <button v-if="accountRestricted" type="button" class="guest-login" @click="emit('open-login')">
            切换 B 站账号
          </button>
          <button v-else-if="inlinePlaybackError.kind === 'region_restricted' && sourceURL" type="button" class="guest-login" @click="BrowserOpenURL(sourceURL)">
            在 B 站查看
          </button>
          <button v-else type="button" class="guest-login" @click="refreshQualities">重新检查</button>
        </div>

        <!-- Download Action & Options Area -->
        <div class="action-footer">
          <div v-if="linkedEpisode" class="options-wrapper">
            <div class="select-group">
              <label for="parse-quality" class="select-label">清晰度</label>
              <AppSelect
                id="parse-quality"
                v-model="displayedQuality"
                class="quality-select"
                :options="qualitySelectOptions"
                :disabled="isFetchingQualities"
                aria-label="清晰度"
              />
            </div>
          </div>

          <!-- Actions Button -->
          <div class="action-btn-wrapper">
            <button v-if="linkedEpisode" class="btn-primary main-action-btn" :disabled="singleDownloadBlocked" @click="handleSingleDownload">
              <Download :size="15" />
              <span>{{ isFetchingQualities ? '检查播放权限…' : (parsedDetail.isCollection ? (parsedDetail.type === 'cheese' ? '下载这一课' : '下载这一集') : '立即下载') }}</span>
            </button>
            <button
              v-if="parsedDetail.isCollection || parsedDetail.totalParts > 1"
              :class="[linkedEpisode ? 'btn-secondary' : 'btn-primary', 'main-action-btn']"
              @click="handleOpenEpisodes"
            >
              <Layers :size="15" />
              <span>{{ parsedDetail.type === 'cheese' ? '选择课时' : '选择分集' }} (共 {{ parsedDetail.totalParts }} {{ parsedDetail.type === 'cheese' ? '课时' : '集' }})</span>
            </button>

          </div>
        </div>
      </div>
    </div>
    <div v-if="parsedDetail && isGuest" class="guest-notice">
      <div class="guest-copy">
        <UserRound :size="15" aria-hidden="true" />
        <strong>未登录 B 站</strong>
        <span>{{ guestLoginDescription }}</span>
      </div>
      <button type="button" class="guest-login" @click="emit('open-login')">登录 B 站账号</button>
    </div>

    <!-- Empty / Capability Cue -->
    <div v-if="!parsedDetail" class="capability-row">
      <span>8K / 4K / 1080P60</span>
      <span class="cap-dot" aria-hidden="true">·</span>
      <span>杜比 / 无损音质</span>
      <span class="cap-dot" aria-hidden="true">·</span>
      <span>合集批量选集</span>
    </div>
  </div>
</template>

<style scoped>
.parse-container {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 18px;
  width: 100%;
  max-width: 880px;
  margin: 0 auto;
}

.parse-container.is-empty {
  min-height: calc(100vh - 120px);
  justify-content: center;
  align-items: center;
  padding-bottom: 64px;
  gap: 0;
}

.parse-container.is-empty::before {
  content: '';
  position: absolute;
  width: 460px;
  height: 220px;
  border-radius: 50%;
  background: radial-gradient(circle, rgba(242, 93, 142, 0.11) 0%, transparent 70%);
  filter: blur(40px);
  pointer-events: none;
  z-index: 0;
}

.hero-head {
  position: relative;
  z-index: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  margin-bottom: 24px;
}

.hero-icon-badge {
  width: 46px;
  height: 46px;
  border-radius: 13px;
  background: rgba(242, 93, 142, 0.12);
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.08);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--bili-pink);
  margin-bottom: 14px;
}

.hero-title {
  font-size: 22px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: -0.025em;
  margin-bottom: 6px;
}

.hero-subtitle {
  font-size: 13px;
  color: var(--text-muted);
}

.input-card {
  position: relative;
  z-index: 1;
  width: 100%;
  border-radius: var(--radius-md);
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  padding: 5px 8px;
  box-shadow: var(--surface-highlight), var(--shadow-md);
  transition: border-color var(--transition-fast), box-shadow var(--transition-fast);
}

.parse-container.is-empty .input-card {
  max-width: 680px;
}

.capability-row {
  position: relative;
  z-index: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 16px;
  font-size: 12px;
  color: var(--text-muted);
}

.cap-dot {
  opacity: 0.45;
}

.input-card:focus-within {
  border-color: var(--border-hover);
  box-shadow: 0 0 0 2px var(--bili-pink-glow);
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
  color: var(--text-primary);
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
  background: var(--neutral-05);
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
  gap: 22px;
  padding: 20px;
  border-radius: var(--radius-md);
  background: var(--bg-card);
  border: 1px solid var(--border-hairline);
  box-shadow: var(--surface-highlight), var(--shadow-md);
}

.card-left {
  width: 264px;
  min-width: 264px;
}

.cover-wrapper {
  position: relative;
  width: 100%;
  aspect-ratio: 16 / 10;
  border-radius: var(--radius-sm);
  overflow: hidden;
  background: rgba(0, 0, 0, 0.35);
  box-shadow: var(--cover-ring);
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
  font-size: 17px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: -0.02em;
  line-height: 1.4;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.playback-notice { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 12px; margin: 0 0 10px; padding: 6px 10px; border-radius: var(--radius-sm); background: var(--neutral-05); font-size: 12px; }
.notice-message { display: flex; align-items: flex-start; gap: 6px; color: var(--guest-accent); font-weight: 600; }
.notice-message svg { flex-shrink: 0; margin-top: 2px; }

.guest-notice {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 12px;
  margin-top: -6px;
  padding: 8px 14px;
  border-radius: var(--radius-sm);
  background: var(--guest-notice-bg);
  font-size: 12px;
}

.guest-copy { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; color: var(--guest-accent); }
.guest-copy span { color: var(--text-secondary); line-height: 1.5; }
.guest-login {
  margin-left: auto;
  padding: 5px 9px;
  border: 1px solid color-mix(in srgb, var(--guest-accent) 24%, transparent);
  border-radius: var(--radius-xs);
  background: var(--guest-login-bg);
  color: var(--guest-accent);
  font-size: 12px;
  font-weight: 600;
  white-space: nowrap;
}
.guest-login:hover { background: color-mix(in srgb, var(--guest-accent) 16%, transparent); }
.guest-login:focus-visible { outline: 2px solid var(--guest-accent); outline-offset: 2px; }

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
  flex-wrap: wrap;
  padding-top: 10px;
}

.options-wrapper {
  display: flex;
  align-items: center;
  gap: 12px;
}

.select-group {
  display: flex;
  align-items: center;
  gap: 8px;
}

.select-label {
  font-size: 12px;
  color: var(--text-secondary);
  white-space: nowrap;
}

.quality-select {
  width: 210px;
}

.action-btn-wrapper {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-left: auto;
  flex-shrink: 0;
}

.main-action-btn {
  height: 34px;
  padding: 0 18px;
  font-size: 13px;
  white-space: nowrap;
}
.main-action-btn:disabled { opacity: .5; cursor: not-allowed; box-shadow: none; }
</style>
