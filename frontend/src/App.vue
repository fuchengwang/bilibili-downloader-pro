<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import confetti from 'canvas-confetti'
import { AlertTriangle, Trash2, X } from 'lucide-vue-next'
import Sidebar from './components/Sidebar.vue'
import Header from './components/Header.vue'
import QuickParse from './components/QuickParse.vue'
import DownloadQueue from './components/DownloadQueue.vue'
import HistoryList from './components/HistoryList.vue'
import SettingsView from './components/SettingsView.vue'
import EpisodeSelectorModal from './components/EpisodeSelectorModal.vue'
import LoginModal from './components/LoginModal.vue'
import Toast, { ToastItem } from './components/Toast.vue'

import { bilibili, downloader, config } from '../wailsjs/go/models'
import {
  GetTasks,
  GetSettings,
  GetUserInfo,
  AddDownloadTasks,
  PauseTask,
  ResumeTask,
  CancelTask,
  DeleteTask,
  PauseAllTasks,
  ResumeAllTasks,
  ClearCompletedTasks,
  OpenDirectory,
  OpenFile,
  ReadClipboard,
  CheckLicense,
  ActivateLicense
} from '../wailsjs/go/main/App'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { theme, normalizeTheme } from './theme'

// State
const activeTab = ref<'parse' | 'queue' | 'history' | 'settings'>('parse')
const tasks = ref<downloader.DownloadTask[]>([])
const unreadCompletedCount = ref(0)
const userInfo = ref<bilibili.UserInfo | null>(null)
const settings = ref<config.Settings>({
  downloadDir: '',
  defaultQuality: 'highest',
  defaultCodec: 'auto',
  maxConcurrent: 3,
  threadsPerTask: 4,
  autoMerge: true,
  deleteTempFiles: true,
  autoClipboard: true,
  fileNameTemplate: '{title} - {part}',
  theme: 'dark'
})
const licenseChecking = ref(true)
const isLicensed = ref(false)
const licenseKey = ref('')
const licenseMessage = ref('')
const licenseStatus = ref<any>(null)
const isActivating = ref(false)
let appInitialized = false

// Modals
const showLoginModal = ref(false)
const showEpisodeModal = ref(false)
const activeEpisodeDetail = ref<bilibili.VideoDetail | null>(null)
const activeEpisodeQuality = ref('highest')
const activeEpisodeCodec = ref('auto')

// Missing File Modal
const showMissingFileModal = ref(false)
const missingFileTask = ref<downloader.DownloadTask | null>(null)

// Toasts
const toasts = ref<ToastItem[]>([])

// QuickParse ref
const quickParseRef = ref<any>(null)

// 剪贴板自动感应与解析
let clipboardTimer: any = null
let lastParsedClipboardText = ''
let isCheckingClipboard = false

async function checkAndAutoParseClipboard() {
  if (!settings.value.autoClipboard || isCheckingClipboard) return
  isCheckingClipboard = true
  try {
    const text = (await ReadClipboard())?.trim()
    if (!text || text === lastParsedClipboardText) return

    // 判断是否包含 B站 特征
    const isBili = /(bilibili\.com|b23\.tv|BV1[a-zA-Z0-9]{9}|av\d+|ep\d+|ss\d+)/i.test(text)
    if (isBili) {
      activeTab.value = 'parse'
      await nextTick()
      if (quickParseRef.value?.setAndParse) {
        lastParsedClipboardText = text
        quickParseRef.value.setAndParse(text)
        showToast(`检测到剪贴板链接，已自动填入解析: ${text.substring(0, 32)}...`, 'info')
      }
    }
  } catch (e) {
    // ignore
  } finally {
    isCheckingClipboard = false
  }
}

// 轮询与事件监听
let taskTimer: any = null

async function initializeAuthorizedApp() {
  if (appInitialized) return
  appInitialized = true
  // 每项初始化独立处理：B站用户信息暂时请求失败时，不能阻止本地任务队列显示。
  try {
    await loadSettings()
  } catch (err) {
    console.error('加载偏好设置失败:', err)
  }

  try {
    const u = await GetUserInfo()
    if (u) {
      userInfo.value = u
    }
  } catch (err) {
    console.error('加载用户信息失败:', err)
  }

  try {
    const tList = await GetTasks()
    if (tList) {
      tasks.value = tList
    }
  } catch (err) {
    console.error('加载任务列表失败:', err)
  }

  // 4. 监听后端事件
  EventsOn('task:update', (updatedTask: downloader.DownloadTask) => {
    const idx = tasks.value.findIndex(t => t.id === updatedTask.id)
    if (idx !== -1) {
      tasks.value[idx] = updatedTask
    } else {
      tasks.value.unshift(updatedTask)
    }
  })

  EventsOn('task:completed', (completedTask: downloader.DownloadTask) => {
    const idx = tasks.value.findIndex(t => t.id === completedTask.id)
    if (idx !== -1) {
      tasks.value[idx] = completedTask
    }
    if (activeTab.value !== 'history') {
      unreadCompletedCount.value++
    }
    showToast(`下载完成: ${completedTask.title}`, 'success')
    triggerConfetti()
  })

  EventsOn('task:error', (errorTask: downloader.DownloadTask) => {
    const idx = tasks.value.findIndex(t => t.id === errorTask.id)
    if (idx !== -1) {
      tasks.value[idx] = errorTask
    }
    showToast(`下载出错: ${errorTask.title} (${errorTask.errorMsg})`, 'error')
  })

  // 5. 监听窗口重新聚焦与唤醒事件，用户复制链接切回软件时毫秒级自动感应，杜绝后台无谓轮询触发系统隐私告警
  window.addEventListener('focus', checkAndAutoParseClipboard)
  EventsOn('app:wakeup', checkAndAutoParseClipboard)
  nextTick(checkAndAutoParseClipboard)
}

function friendlyLicenseMessage(message: string) {
  if (!message) return '激活失败，请检查激活码后重试'
  if (message.includes('cannot connect') || message.includes('unreachable') || message.includes('timeout')) {
    return '暂时无法连接激活服务器，请检查网络后重试'
  }
  if (message.includes('invalid license') || message.includes('卡密不存在')) {
    return '激活码无效，请检查是否输入正确'
  }
  return message
}

async function checkLicense() {
  licenseChecking.value = true
  try {
    const result: any = await CheckLicense(false)
    isLicensed.value = !!result?.success
    licenseStatus.value = result?.data || null
    licenseMessage.value = result?.success ? '' : friendlyLicenseMessage(result?.message || '')
    if (isLicensed.value) await initializeAuthorizedApp()
  } catch (err: any) {
    isLicensed.value = false
    licenseMessage.value = friendlyLicenseMessage(err?.message || String(err))
  } finally {
    licenseChecking.value = false
  }
}

async function activateLicense() {
  const key = licenseKey.value.trim()
  if (!key || isActivating.value) return
  isActivating.value = true
  licenseMessage.value = ''
  try {
    const result: any = await ActivateLicense(key)
    if (!result?.success) {
      licenseMessage.value = friendlyLicenseMessage(result?.message || '')
      return
    }
    isLicensed.value = true
    licenseStatus.value = result?.data || null
    licenseKey.value = ''
    await initializeAuthorizedApp()
  } catch (err: any) {
    licenseMessage.value = friendlyLicenseMessage(err?.message || String(err))
  } finally {
    isActivating.value = false
  }
}

async function loadSettings() {
  const s = await GetSettings()
  if (s) {
    settings.value = s
    theme.value = normalizeTheme(s.theme)
  }
}

function handleThemeChanged(value: string) {
  settings.value.theme = value
  theme.value = normalizeTheme(value)
}

function handleSettingsSaved(s: config.Settings) {
  settings.value = { ...s }
  theme.value = normalizeTheme(s.theme)
}

onMounted(async () => {
  // Theme preferences are available even on the activation screen.
  try { await loadSettings() } catch (err) { console.error('加载外观设置失败:', err) }
  await checkLicense()
})

onUnmounted(() => {
  if (clipboardTimer) clearInterval(clipboardTimer)
  window.removeEventListener('focus', checkAndAutoParseClipboard)
})

function triggerConfetti() {
  try {
    confetti({
      particleCount: 50,
      spread: 60,
      origin: { y: 0.8 },
      colors: ['#FB7299', '#00AEEC', '#10B981', '#F59E0B']
    })
  } catch (e) {}
}

function handleClipboardAction(url: string, toastId: string) {
  closeToast(toastId)
  activeTab.value = 'parse'
  if (quickParseRef.value?.setAndParse) {
    quickParseRef.value.setAndParse(url)
  }
}

function showToast(message: string, type: 'success' | 'error' | 'info' = 'info') {
  const id = Math.random().toString()
  toasts.value.push({ id, message, type })
  setTimeout(() => {
    closeToast(id)
  }, 3500)
}

function closeToast(id: string) {
  toasts.value = toasts.value.filter(t => t.id !== id)
}

// Tab 切换
function handleTabChange(tab: string) {
  activeTab.value = tab as any
  if (tab === 'history') {
    unreadCompletedCount.value = 0
  }
}

// 计算活跃任务数与总速度
const activeTasks = computed(() => {
  return tasks.value.filter(t => t.status === 'downloading' || t.status === 'merging')
})

const completedTasks = computed(() => {
  return tasks.value.filter(t => t.status === 'completed')
})

const totalSpeedStr = computed(() => {
  let totalBps = 0
  tasks.value.forEach(t => {
    if (t.status === 'downloading') totalBps += (t.speed || 0)
  })
  if (totalBps <= 0) return '0 KB/s'
  const unit = 1024
  if (totalBps < unit) return totalBps + ' B/s'
  const exp = Math.floor(Math.log(totalBps) / Math.log(unit))
  const size = totalBps / Math.pow(unit, exp)
  return size.toFixed(1) + ' ' + 'KMGTPE'[exp - 1] + 'B/s'
})

const currentTabTitle = computed(() => {
  switch (activeTab.value) {
    case 'parse': return '视频解析与下载'
    case 'queue': return '下载任务队列'
    case 'history': return '已完成下载历史'
    case 'settings': return '偏好设置'
    default: return '哔哩下载器专业版'
  }
})

// 选集模态框交互
function handleOpenEpisodes(detail: bilibili.VideoDetail, quality: string, codec: string) {
  activeEpisodeDetail.value = detail
  activeEpisodeQuality.value = quality
  activeEpisodeCodec.value = codec
  showEpisodeModal.value = true
}

// 单集快速下载
async function handleQuickDownloadSingle(
  detail: bilibili.VideoDetail,
  ep: bilibili.EpisodeInfo,
  quality: string,
  codec: string
) {
  try {
    const req: any = {
      bvid: ep.bvid || detail.bvid,
      aid: ep.aid || detail.aid,
      epid: ep.epid || 0,
      title: detail.title,
      cover: ep.cover || detail.cover,
      isBangumi: detail.type === 'bangumi',
      isCheese: detail.type === 'cheese',
      ssid: detail.seasonId || 0,
      targetQuality: quality,
      targetCodec: codec,
      episodes: [ep.cid],
    }
    const added = await AddDownloadTasks(req)
    showDownloadResults(added || [])
  } catch (err: any) {
    showToast('添加任务失败: ' + err.message, 'error')
  }
}

// 批量提交分集下载
async function handleEpisodeBatchSubmit(selectedCids: number[], quality: string, codec: string) {
  if (!activeEpisodeDetail.value || selectedCids.length === 0) return

  try {
    const detail = activeEpisodeDetail.value
    const firstEpWithEpid = detail.episodes?.find((e: any) => e.epid > 0)
    const req: any = {
      bvid: detail.bvid,
      aid: detail.aid,
      epid: firstEpWithEpid?.epid || 0,
      title: detail.title,
      cover: detail.cover,
      isBangumi: detail.type === 'bangumi',
      isCheese: detail.type === 'cheese',
      ssid: detail.seasonId || 0,
      targetQuality: quality,
      targetCodec: codec,
      episodes: selectedCids,
    }
    const added = await AddDownloadTasks(req)
    showEpisodeModal.value = false
    showDownloadResults(added || [])
  } catch (err: any) {
    showToast('批量添加任务失败: ' + err.message, 'error')
  }
}

function showDownloadResults(results: downloader.DownloadTask[]) {
  if (results.length === 0) return
  for (const task of results) {
    const idx = tasks.value.findIndex(t => t.id === task.id)
    if (idx >= 0) tasks.value[idx] = task
    else tasks.value.unshift(task)
  }
  const completed = results.filter(t => t.status === 'completed').length
  const pending = results.length - completed
  if (pending === 0) {
    showToast('已下载，文件已存在，可在下载历史中播放', 'success')
    activeTab.value = 'history'
  } else {
    showToast(completed > 0
      ? `${pending} 个任务在下载队列中，${completed} 个已下载`
      : `${pending} 个任务已在下载队列中`, 'success')
    activeTab.value = 'queue'
  }
}

// 队列操作
async function onPauseTask(id: string) {
  try {
    await PauseTask(id)
  } catch (err: any) {
    showToast('暂停失败: ' + (err?.message || err), 'error')
  }
}

async function onResumeTask(id: string) {
  try {
    await ResumeTask(id)
  } catch (err: any) {
    showToast('继续失败: ' + (err?.message || err), 'error')
  }
}

async function onCancelTask(id: string) {
  try {
    await CancelTask(id)
  } catch (err: any) {
    showToast('取消失败: ' + (err?.message || err), 'error')
  }
}

async function onDeleteTask(id: string, deleteFile: boolean = false) {
  try {
    await DeleteTask(id, deleteFile)
    tasks.value = tasks.value.filter(t => t.id !== id)
    showToast(deleteFile ? '已删除记录及本地文件' : '已清除该记录', 'info')
  } catch (e: any) {
    showToast('删除失败: ' + (e?.message || e), 'error')
  }
}

async function confirmMissingFileClean() {
  if (!missingFileTask.value) return
  const id = missingFileTask.value.id
  try {
    await DeleteTask(id, false)
    tasks.value = tasks.value.filter(t => t.id !== id)
    showToast('已从已完成列表中清除该条记录', 'info')
  } catch (e: any) {
    showToast('清除记录失败: ' + (e?.message || e), 'error')
  } finally {
    showMissingFileModal.value = false
    missingFileTask.value = null
  }
}

async function onPauseAll() {
  try {
    await PauseAllTasks()
  } catch (err: any) {
    showToast('全部暂停失败: ' + (err?.message || err), 'error')
  }
}

async function onResumeAll() {
  try {
    await ResumeAllTasks()
  } catch (err: any) {
    showToast('全部继续失败: ' + (err?.message || err), 'error')
  }
}

async function onClearCompleted(deleteFiles: boolean = false) {
  try {
    await ClearCompletedTasks(deleteFiles)
    tasks.value = tasks.value.filter(t => t.status !== 'completed' && t.status !== 'cancelled')
    showToast(deleteFiles ? '已清空记录并删除本地文件' : '已清空完成记录', 'info')
  } catch (e: any) {
    showToast('清空失败: ' + (e?.message || e), 'error')
  }
}

async function onOpenDir(path: string) {
  try {
    await OpenDirectory(path)
  } catch (e: any) {
    showToast('打开目录失败: ' + e.message, 'error')
  }
}

async function onOpenFile(path: string, task?: downloader.DownloadTask) {
  try {
    await OpenFile(path)
  } catch (e: any) {
    const msg = String(e?.message || e || '')
    if (task && (msg.includes('FILE_NOT_FOUND') || msg.includes('不存在') || msg.includes('系统找不到指定的文件'))) {
      missingFileTask.value = task
      showMissingFileModal.value = true
      return
    }
    showToast('打开文件失败: ' + (e?.message || e), 'error')
  }
}

function handleLoginSuccess(u: bilibili.UserInfo) {
  userInfo.value = u
  showToast(`登录成功: ${u.uname || 'B站用户'}`, 'success')
  if (quickParseRef.value?.refreshQualities) {
    quickParseRef.value.refreshQualities()
  }
}

function handleLogoutSuccess() {
  userInfo.value = null
  showToast('已退出登录', 'info')
  if (quickParseRef.value?.refreshQualities) {
    quickParseRef.value.refreshQualities()
  }
}

function handleLicenseDeactivated() {
  isLicensed.value = false
  licenseStatus.value = null
  licenseMessage.value = '本机已解绑，可以输入其他激活码'
  activeTab.value = 'parse'
}
</script>

<template>
  <div v-if="isLicensed" class="app-layout">
    <!-- Left Navigation Sidebar -->
    <Sidebar
      :active-tab="activeTab"
      :user-info="userInfo"
      :active-task-count="activeTasks.length"
      :completed-task-count="unreadCompletedCount"
      @change-tab="handleTabChange"
      @open-login="showLoginModal = true"
    />

    <!-- Main Content Area -->
    <div class="main-wrapper">
      <Header
        :title="currentTabTitle"
        :total-speed-str="totalSpeedStr"
        :auto-clipboard="settings.autoClipboard"
        @open-dir="onOpenDir(settings.downloadDir)"
      />

      <main class="page-content">
        <!-- View 1: Quick Parse -->
        <div v-show="activeTab === 'parse'" class="tab-view">
          <QuickParse
            ref="quickParseRef"
            :default-quality="settings.defaultQuality"
            :default-codec="settings.defaultCodec"
            :user-info="userInfo"
            @open-episodes="handleOpenEpisodes"
            @quick-download-single="handleQuickDownloadSingle"
            @show-toast="showToast"
          />
        </div>

        <!-- View 2: Download Queue -->
        <div v-show="activeTab === 'queue'" class="tab-view">
          <DownloadQueue
            :tasks="tasks"
            @pause-task="onPauseTask"
            @resume-task="onResumeTask"
            @cancel-task="onCancelTask"
            @delete-task="onDeleteTask"
            @open-dir="onOpenDir"
            @open-file="onOpenFile"
            @pause-all="onPauseAll"
            @resume-all="onResumeAll"
            @clear-completed="onClearCompleted"
          />
        </div>

        <!-- View 3: History -->
        <div v-show="activeTab === 'history'" class="tab-view">
          <HistoryList
            :tasks="tasks"
            @open-file="onOpenFile"
            @open-dir="onOpenDir"
            @delete-task="onDeleteTask"
            @clear-completed="onClearCompleted"
          />
        </div>

        <!-- View 4: Settings (In-Page View, Not Modal!) -->
        <div v-show="activeTab === 'settings'" class="tab-view">
          <SettingsView
            :initial-settings="settings"
            :license-status="licenseStatus"
            @settings-saved="handleSettingsSaved"
            @theme-changed="handleThemeChanged"
            @show-toast="showToast"
            @license-deactivated="handleLicenseDeactivated"
          />
        </div>
      </main>
    </div>

    <!-- Modals -->
    <EpisodeSelectorModal
      v-if="showEpisodeModal && activeEpisodeDetail"
      :detail="activeEpisodeDetail"
      :initial-quality="activeEpisodeQuality"
      :initial-codec="activeEpisodeCodec"
      @close="showEpisodeModal = false"
      @submit="handleEpisodeBatchSubmit"
    />

    <LoginModal
      v-if="showLoginModal"
      :user-info="userInfo"
      @close="showLoginModal = false"
      @login-success="handleLoginSuccess"
      @logout-success="handleLogoutSuccess"
      @show-toast="showToast"
    />

    <!-- Modal 1: Missing File Clean Confirmation -->
    <div v-if="showMissingFileModal && missingFileTask" class="modal-backdrop" @click.self="showMissingFileModal = false">
      <div class="confirm-dialog">
        <div class="dialog-header">
          <div class="dialog-icon-wrap warn">
            <AlertTriangle :size="20" />
          </div>
          <div class="dialog-title-box">
            <h3 class="dialog-title">本地文件不存在</h3>
            <p class="dialog-desc">检测到该视频文件已在本地磁盘被移动或删除</p>
          </div>
          <button class="btn-close" @click="showMissingFileModal = false">
            <X :size="16" />
          </button>
        </div>

        <div class="dialog-body">
          <div class="file-info-card">
            <div class="file-name">{{ missingFileTask.title }}</div>
            <div v-if="missingFileTask.partTitle && missingFileTask.partTitle !== missingFileTask.title" class="file-sub">{{ missingFileTask.partTitle }}</div>
            <div class="file-path" :title="missingFileTask.outputPath">{{ missingFileTask.outputPath }}</div>
          </div>
          <p class="dialog-question">是否从已完成列表中清除此条无效记录？</p>
        </div>

        <div class="dialog-footer">
          <button class="btn-secondary" @click="showMissingFileModal = false">保留记录</button>
          <button class="btn-danger" @click="confirmMissingFileClean">清除记录</button>
        </div>
      </div>
    </div>

    <!-- Global Toast Notifications -->
    <Toast
      :toasts="toasts"
      @close-toast="closeToast"
      @action-clipboard="handleClipboardAction"
    />
  </div>

  <div v-else class="license-screen">
    <div class="license-glow license-glow-pink"></div>
    <div class="license-glow license-glow-blue"></div>
    <form class="license-card" @submit.prevent="activateLicense">
      <img src="./assets/images/logo-universal.png" class="license-logo" alt="BBDown Pro" />
      <div class="license-eyebrow">BBDOWN PRO</div>
      <h1>{{ licenseChecking ? '正在检查授权' : '激活专业版' }}</h1>
      <p class="license-copy">
        {{ licenseChecking ? '正在读取本机授权，请稍候…' : '首次激活需要联网，成功后可在断网环境正常使用。' }}
      </p>
      <template v-if="!licenseChecking">
        <label class="license-field">
          <span>激活码</span>
          <input
            v-model="licenseKey"
            type="text"
            autocomplete="off"
            spellcheck="false"
            placeholder="例如：PRO-XXXX-XXXX-XXXX"
            autofocus
          />
        </label>
        <p v-if="licenseMessage" class="license-error">{{ licenseMessage }}</p>
        <button class="btn-primary license-submit" type="submit" :disabled="!licenseKey.trim() || isActivating">
          {{ isActivating ? '正在激活…' : '立即激活' }}
        </button>
        <p class="license-help">没有激活码？请联系卖家购买。换机时可由卖家在后台解绑旧设备。</p>
      </template>
      <div v-else class="license-loader"></div>
    </form>
  </div>
</template>

<style scoped>
.license-screen {
  position: relative;
  width: 100vw;
  height: 100vh;
  overflow: hidden;
  display: grid;
  place-items: center;
  background: var(--bg-app);
}

.license-glow {
  position: absolute;
  width: 360px;
  height: 360px;
  border-radius: 50%;
  filter: blur(100px);
  opacity: 0.13;
}

.license-glow-pink { left: -100px; top: -120px; background: var(--bili-pink); }
.license-glow-blue { right: -100px; bottom: -120px; background: var(--bili-blue); }

.license-card {
  position: relative;
  z-index: 1;
  width: min(410px, calc(100vw - 48px));
  padding: 32px;
  border-radius: 22px;
  border: 1px solid var(--border-subtle);
  background: var(--bg-card);
  box-shadow: var(--shadow-lg);
  backdrop-filter: blur(20px);
  text-align: center;
}

.license-logo { width: 68px; height: 68px; object-fit: contain; margin-bottom: 10px; }
.license-eyebrow { color: var(--bili-pink); font-size: 10px; font-weight: 800; letter-spacing: 2.4px; }
.license-card h1 { margin: 9px 0 7px; font-size: 24px; color: var(--text-primary); }
.license-copy { margin: 0 0 22px; color: var(--text-secondary); font-size: 13px; line-height: 1.65; }
.license-field { display: block; text-align: left; }
.license-field span { display: block; margin-bottom: 7px; color: var(--text-secondary); font-size: 12px; font-weight: 600; }
.license-field input { width: 100%; height: 43px; padding: 0 13px; text-transform: uppercase; letter-spacing: 0.6px; }
.license-error { margin: 10px 0 0; color: var(--danger); font-size: 12px; line-height: 1.5; text-align: left; }
.license-submit { width: 100%; height: 42px; margin-top: 16px; }
.license-submit:disabled { opacity: 0.5; cursor: default; }
.license-help { margin: 15px 0 0; color: var(--text-muted); font-size: 11px; line-height: 1.6; }
.license-loader { width: 24px; height: 24px; margin: 8px auto 0; border: 2px solid var(--neutral-12); border-top-color: var(--bili-pink); border-radius: 50%; animation: licenseSpin .7s linear infinite; }
@keyframes licenseSpin { to { transform: rotate(360deg); } }

.app-layout {
  display: flex;
  width: 100vw;
  height: 100vh;
  overflow: hidden;
  background-color: var(--bg-app);
}

.main-wrapper {
  flex: 1;
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
  position: relative;
  min-width: 0;
}

.page-content {
  flex: 1;
  overflow-y: auto;
  padding: 18px 24px 30px 24px;
}

.tab-view {
  width: 100%;
  animation: fadeIn 0.2s ease-out;
}

/* Modal Dialogs */
.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.65);
  backdrop-filter: blur(12px);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  animation: fadeIn 0.15s ease-out;
}

.confirm-dialog {
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  width: 440px;
  max-width: 90vw;
  box-shadow: 0 20px 40px rgba(0, 0, 0, 0.4);
  overflow: hidden;
  animation: scaleUp 0.15s ease-out;
}

.dialog-header {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 18px 20px 14px 20px;
  border-bottom: 1px solid var(--border-subtle);
}

.dialog-icon-wrap {
  width: 36px;
  height: 36px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.dialog-icon-wrap.warn {
  background: rgba(245, 158, 11, 0.15);
  color: #f59e0b;
}

.dialog-icon-wrap.delete {
  background: rgba(239, 68, 68, 0.15);
  color: #ef4444;
}

.dialog-title-box {
  flex: 1;
}

.dialog-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0;
}

.dialog-desc {
  font-size: 12px;
  color: var(--text-secondary);
  margin: 2px 0 0 0;
}

.btn-close {
  background: transparent;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  padding: 4px;
  border-radius: var(--radius-sm);
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all var(--transition-fast);
}

.btn-close:hover {
  background: var(--neutral-08);
  color: var(--text-primary);
}

.dialog-body {
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.file-info-card {
  background: var(--neutral-03);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  padding: 10px 12px;
}

.file-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-primary);
  word-break: break-all;
}

.file-sub {
  font-size: 12px;
  color: var(--bili-pink);
  margin-top: 2px;
}

.file-path {
  font-size: 11px;
  color: var(--text-muted);
  font-family: monospace;
  margin-top: 6px;
  word-break: break-all;
}

.dialog-question {
  font-size: 13px;
  color: var(--text-secondary);
  margin: 0;
}

.checkbox-option {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text-secondary);
  cursor: pointer;
  margin-top: 4px;
  user-select: none;
}

.custom-chk {
  width: 16px;
  height: 16px;
  accent-color: var(--bili-pink);
  cursor: pointer;
}

.dialog-footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  padding: 12px 20px;
  background: rgba(0, 0, 0, 0.15);
  border-top: 1px solid var(--border-subtle);
}

.btn-danger {
  background: #ef4444;
  color: #fff;
  border: none;
  padding: 6px 14px;
  border-radius: var(--radius-sm);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: all var(--transition-fast);
}

.btn-danger:hover {
  background: #dc2626;
}
</style>
