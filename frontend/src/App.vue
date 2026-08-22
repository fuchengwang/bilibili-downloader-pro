<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import confetti from 'canvas-confetti'
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
  ReadClipboard
} from '../wailsjs/go/main/App'
import { EventsOn } from '../wailsjs/runtime/runtime'

// State
const activeTab = ref<'parse' | 'queue' | 'history' | 'settings'>('parse')
const tasks = ref<downloader.DownloadTask[]>([])
const userInfo = ref<bilibili.UserInfo | null>(null)
const settings = ref<config.Settings>({
  downloadDir: '',
  defaultQuality: 'highest',
  defaultCodec: 'auto',
  maxConcurrent: 3,
  threadsPerTask: 4,
  autoMerge: true,
  deleteTempFiles: true,
  ffmpegPath: '',
  autoClipboard: false,
  fileNameTemplate: '{title} - {part}',
  theme: 'dark'
})

// Modals
const showLoginModal = ref(false)
const showEpisodeModal = ref(false)
const activeEpisodeDetail = ref<bilibili.VideoDetail | null>(null)
const activeEpisodeQuality = ref('highest')
const activeEpisodeCodec = ref('auto')

// Toasts
const toasts = ref<ToastItem[]>([])

// QuickParse ref
const quickParseRef = ref<any>(null)

// Clipboard Watcher
let clipboardTimer: any = null
let lastClipboardText = ''

onMounted(async () => {
  // 1. 初始化设置
  try {
    const s = await GetSettings()
    if (s) settings.value = s
  } catch (e) {}

  // 2. 初始化任务列表
  try {
    const tList = await GetTasks()
    if (tList) tasks.value = tList
  } catch (e) {}

  // 3. 获取用户登录状态
  try {
    const user = await GetUserInfo()
    if (user) userInfo.value = user
  } catch (e) {}

  // 4. 监听后端下载进度事件
  EventsOn('task:progress', (updatedTask: downloader.DownloadTask) => {
    handleTaskProgress(updatedTask)
  })

  // 5. 启动剪贴板监听
  startClipboardWatcher()
})

onUnmounted(() => {
  if (clipboardTimer) clearInterval(clipboardTimer)
})

function handleTaskProgress(updatedTask: downloader.DownloadTask) {
  const idx = tasks.value.findIndex(t => t.id === updatedTask.id)
  if (idx !== -1) {
    const oldStatus = tasks.value[idx].status
    tasks.value[idx] = updatedTask

    // 如果任务刚完成，播放完成撒花动画与提示
    if (oldStatus !== 'completed' && updatedTask.status === 'completed') {
      triggerConfetti()
      showToast(`视频下载完成: ${updatedTask.partTitle || updatedTask.title}`, 'success')
    }
  } else {
    tasks.value.unshift(updatedTask)
  }
}

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

// 剪贴板轮询检测
function startClipboardWatcher() {
  clipboardTimer = setInterval(async () => {
    if (!settings.value.autoClipboard) return
    try {
      const text = (await ReadClipboard())?.trim()
      if (!text || text === lastClipboardText) return
      lastClipboardText = text

      // 判断是否包含 B站 特征
      const isBili = /(bilibili\.com|b23\.tv|BV1[a-zA-Z0-9]{9}|av\d+|ep\d+|ss\d+)/i.test(text)
      if (isBili) {
        showClipboardToast(text)
      }
    } catch (e) {}
  }, 2500)
}

function showClipboardToast(url: string) {
  if (toasts.value.some(t => t.type === 'clipboard' && t.actionUrl === url)) return

  toasts.value.push({
    id: Math.random().toString(),
    message: '检测到剪贴板中的 B站 链接，是否立即解析？',
    type: 'clipboard',
    actionUrl: url,
  })
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
}

// 计算活跃任务数与总速度
const activeTasks = computed(() => {
  return tasks.value.filter(t => t.status === 'downloading' || t.status === 'merging')
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
    const req: downloader.DownloadRequest = {
      bvid: ep.bvid || detail.bvid,
      aid: ep.aid || detail.aid,
      title: detail.title,
      cover: ep.cover || detail.cover,
      isBangumi: detail.type === 'bangumi',
      targetQuality: quality,
      targetCodec: codec,
      episodes: [ep.cid],
    }
    const added = await AddDownloadTasks(req)
    if (added && added.length > 0) {
      showToast(`已加入下载队列: ${ep.title}`, 'success')
      activeTab.value = 'queue'
    }
  } catch (err: any) {
    showToast('添加任务失败: ' + err.message, 'error')
  }
}

// 批量提交分集下载
async function handleEpisodeBatchSubmit(
  cids: number[],
  quality: string,
  codec: string,
  startNow: boolean
) {
  if (!activeEpisodeDetail.value) return
  showEpisodeModal.value = false

  try {
    const detail = activeEpisodeDetail.value
    const req: downloader.DownloadRequest = {
      bvid: detail.bvid,
      aid: detail.aid,
      title: detail.title,
      cover: detail.cover,
      isBangumi: detail.type === 'bangumi',
      targetQuality: quality,
      targetCodec: codec,
      episodes: cids,
    }

    const added = await AddDownloadTasks(req)
    if (added && added.length > 0) {
      showToast(`已成功添加 ${added.length} 集任务至下载队列！`, 'success')
      if (startNow) {
        activeTab.value = 'queue'
      }
    }
  } catch (err: any) {
    showToast('批量添加失败: ' + err.message, 'error')
  }
}

// 任务控制
async function onPauseTask(id: string) {
  await PauseTask(id)
}

async function onResumeTask(id: string) {
  await ResumeTask(id)
}

async function onCancelTask(id: string) {
  await CancelTask(id)
}

async function onDeleteTask(id: string, deleteFile: boolean) {
  await DeleteTask(id, deleteFile)
  tasks.value = tasks.value.filter(t => t.id !== id)
}

async function onPauseAll() {
  await PauseAllTasks()
}

async function onResumeAll() {
  await ResumeAllTasks()
}

async function onClearCompleted() {
  await ClearCompletedTasks()
  tasks.value = tasks.value.filter(t => t.status !== 'completed' && t.status !== 'cancelled')
  showToast('已清空完成记录', 'info')
}

async function onOpenDir(path: string) {
  try {
    await OpenDirectory(path)
  } catch (e: any) {
    showToast('打开目录失败: ' + e.message, 'error')
  }
}

async function onOpenFile(path: string) {
  try {
    await OpenFile(path)
  } catch (e: any) {
    showToast('打开文件失败: ' + e.message, 'error')
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
</script>

<template>
  <div class="app-layout">
    <!-- Left Navigation Sidebar -->
    <Sidebar
      :active-tab="activeTab"
      :user-info="userInfo"
      :active-task-count="activeTasks.length"
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
            @settings-saved="(s) => settings = s"
            @show-toast="showToast"
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

    <!-- Global Toast Notifications -->
    <Toast
      :toasts="toasts"
      @close-toast="closeToast"
      @action-clipboard="handleClipboardAction"
    />
  </div>
</template>

<style scoped>
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
</style>
