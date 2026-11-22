<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import {
  Play,
  Pause,
  RotateCcw,
  Trash2,
  FolderOpen,
  AlertCircle,
  AlertTriangle,
  Clock,
  Zap,
  HardDrive,
  Film,
  ExternalLink,
  ListChecks,
  Check,
  Minus,
  X,
  LayoutList,
  List
} from 'lucide-vue-next'
import { downloader } from '../../wailsjs/go/models'
import { BrowserOpenURL } from '../../wailsjs/runtime/runtime'
import { episodeURL, isPermissionError } from '../utils/episode-selection'
import {
  orderedQueueTasks,
  pruneSelectedQueueIds,
  summarizeQueueSelection,
  toggleQueueTaskSelection
} from '../utils/task-queue'

const props = withDefaults(defineProps<{
  tasks: downloader.DownloadTask[]
  batchBusy?: boolean
  viewMode?: 'comfortable' | 'compact'
}>(), {
  batchBusy: false,
  viewMode: 'comfortable'
})

const emit = defineEmits<{
  (e: 'pause-task', id: string): void
  (e: 'resume-task', id: string): void
  (e: 'cancel-task', id: string): void
  (e: 'delete-task', id: string, deleteFile: boolean): void
  (e: 'request-delete', task: downloader.DownloadTask): void
  (e: 'open-dir', path: string): void
  (e: 'open-file', path: string, task?: downloader.DownloadTask): void
  (e: 'pause-all'): void
  (e: 'resume-all'): void
  (e: 'batch-pause', ids: string[]): void
  (e: 'batch-resume', ids: string[]): void
  (e: 'batch-delete', ids: string[]): void
  (e: 'update:view-mode', mode: 'comfortable' | 'compact'): void
  (e: 'clear-completed'): void
  (e: 'open-login'): void
}>()

const queueContainerRef = ref<HTMLElement | null>(null)
const currentFilter = ref<'all' | 'downloading' | 'paused' | 'error'>('all')
const selectionMode = ref(false)
const selectedIds = ref<Set<string>>(new Set())
const lastAnchorId = ref<string | null>(null)
const showBatchDeleteModal = ref(false)

function isLegacyPlaybackError(task: downloader.DownloadTask) {
  return !task.errorKind && /解析媒体流失败|DASH|playurl/.test(task.errorMsg || '')
}

function taskSourceURL(task: downloader.DownloadTask) {
  return task.sourceUrl || episodeURL(task, task.isCheese ? 'cheese' : task.isBangumi ? 'bangumi' : 'normal')
}

function normalizeImg(url?: string) {
  if (!url) return ''
  if (url.startsWith('//')) return 'https:' + url
  if (url.startsWith('http://')) return 'https://' + url.substring(7)
  return url
}

const activeQueueCount = computed(() => props.tasks.filter(t => t.status !== 'completed').length)
const downloadingCount = computed(() => props.tasks.filter(t => t.status === 'downloading' || t.status === 'merging').length)
const pausedCount = computed(() => props.tasks.filter(t => t.status === 'paused' || t.status === 'queued').length)
const errorCount = computed(() => props.tasks.filter(t => t.status === 'error' || t.status === 'cancelled').length)

const filteredTasks = computed(() => {
  const activeTasks = orderedQueueTasks(props.tasks)
  if (currentFilter.value === 'all') return activeTasks
  if (currentFilter.value === 'downloading') return activeTasks.filter(t => t.status === 'downloading' || t.status === 'merging')
  if (currentFilter.value === 'paused') return activeTasks.filter(t => t.status === 'paused' || t.status === 'queued')
  if (currentFilter.value === 'error') return activeTasks.filter(t => t.status === 'error' || t.status === 'cancelled')
  return activeTasks
})

const selectionSummary = computed(() => summarizeQueueSelection(filteredTasks.value, selectedIds.value))
const batchDeletePreviewTasks = computed(() => selectionSummary.value.selectedTasks.slice(0, 4))

function isTaskSelected(id: string) {
  return selectedIds.value.has(id)
}

function exitSelectionMode() {
  selectionMode.value = false
  selectedIds.value = new Set()
  lastAnchorId.value = null
  showBatchDeleteModal.value = false
}

function toggleSelectionMode() {
  if (selectionMode.value) exitSelectionMode()
  else selectionMode.value = true
}

function applyTaskSelection(taskId: string, shiftKey: boolean) {
  const visibleIds = filteredTasks.value.map(t => t.id)
  const next = toggleQueueTaskSelection(selectedIds.value, visibleIds, taskId, {
    shiftKey,
    anchorId: lastAnchorId.value
  })
  selectedIds.value = next.selected
  lastAnchorId.value = next.anchorId
}

function handleTaskCheckClick(taskId: string, event: MouseEvent) {
  applyTaskSelection(taskId, event.shiftKey)
}

function handleTaskCardClick(taskId: string, event: MouseEvent) {
  const target = event.target as HTMLElement | null
  if (target?.closest('.task-btn-group, .task-error-help, button, a, summary, details')) return
  const shortcutSelect = event.metaKey || event.ctrlKey || event.shiftKey
  if (!selectionMode.value && !shortcutSelect) return
  if (!selectionMode.value) selectionMode.value = true
  applyTaskSelection(taskId, event.shiftKey)
}

function selectAllVisible() {
  const next = new Set(selectedIds.value)
  for (const task of filteredTasks.value) {
    next.add(task.id)
  }
  selectedIds.value = next
  lastAnchorId.value = filteredTasks.value[filteredTasks.value.length - 1]?.id ?? null
}

function toggleSelectAllVisible() {
  const next = new Set(selectedIds.value)
  if (selectionSummary.value.allSelected) {
    for (const task of filteredTasks.value) {
      next.delete(task.id)
    }
    selectedIds.value = next
    lastAnchorId.value = null
    return
  }
  selectAllVisible()
}

function invertSelection() {
  const next = new Set(selectedIds.value)
  for (const task of filteredTasks.value) {
    if (next.has(task.id)) next.delete(task.id)
    else next.add(task.id)
  }
  selectedIds.value = next
  lastAnchorId.value = null
}

function triggerBatchResume() {
  if (props.batchBusy || selectionSummary.value.resumableIds.length === 0) return
  emit('batch-resume', [...selectionSummary.value.resumableIds])
}

function triggerBatchPause() {
  if (props.batchBusy || selectionSummary.value.pausableIds.length === 0) return
  emit('batch-pause', [...selectionSummary.value.pausableIds])
}

function openBatchDeleteModal() {
  if (props.batchBusy || selectionSummary.value.deletableIds.length === 0) return
  showBatchDeleteModal.value = true
}

function confirmBatchDelete() {
  const ids = [...selectionSummary.value.deletableIds]
  if (ids.length === 0) {
    showBatchDeleteModal.value = false
    return
  }
  const removed = new Set(ids)
  const next = new Set(selectedIds.value)
  for (const id of removed) next.delete(id)
  selectedIds.value = next
  lastAnchorId.value = null
  showBatchDeleteModal.value = false
  emit('batch-delete', ids)
}

watch(() => props.tasks, (nextTasks) => {
  if (selectedIds.value.size > 0) {
    selectedIds.value = pruneSelectedQueueIds(selectedIds.value, nextTasks)
  }
  if (!nextTasks.some(t => t.status !== 'completed')) {
    exitSelectionMode()
  }
  if (currentFilter.value === 'error' && !nextTasks.some(t => t.status === 'error' || t.status === 'cancelled')) {
    currentFilter.value = 'all'
  }
}, { deep: true })

function handleKeydown(event: KeyboardEvent) {
  if (!selectionMode.value) return
  if (!queueContainerRef.value || queueContainerRef.value.getClientRects().length === 0) return
  const target = event.target as HTMLElement | null
  if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return
  if (event.key === 'Escape') {
    event.preventDefault()
    if (showBatchDeleteModal.value) showBatchDeleteModal.value = false
    else exitSelectionMode()
    return
  }
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'a' && filteredTasks.value.length > 0) {
    event.preventDefault()
    selectAllVisible()
  }
}

onMounted(() => {
  window.addEventListener('keydown', handleKeydown)
})

onUnmounted(() => {
  window.removeEventListener('keydown', handleKeydown)
})
</script>

<template>
  <div ref="queueContainerRef" class="queue-container">
    <!-- Queue Header & Action Bar -->
    <div class="queue-header">
      <div class="filter-tabs">
        <button
          class="tab-btn"
          :class="{ active: currentFilter === 'all' }"
          @click="currentFilter = 'all'"
        >
          全部 ({{ activeQueueCount }})
        </button>
        <button
          class="tab-btn"
          :class="{ active: currentFilter === 'downloading' }"
          @click="currentFilter = 'downloading'"
        >
          下载中 ({{ downloadingCount }})
        </button>
        <button
          class="tab-btn"
          :class="{ active: currentFilter === 'paused' }"
          @click="currentFilter = 'paused'"
        >
          等待/暂停 ({{ pausedCount }})
        </button>
        <button
          v-if="errorCount > 0"
          class="tab-btn tab-btn-error"
          :class="{ active: currentFilter === 'error' }"
          @click="currentFilter = 'error'"
        >
          失败 ({{ errorCount }})
        </button>
      </div>

      <div class="queue-actions">
        <div v-if="activeQueueCount > 0" class="view-toggle" role="group" aria-label="视图切换">
          <button
            type="button"
            class="view-toggle-btn"
            :class="{ active: viewMode === 'comfortable' }"
            title="卡片视图"
            aria-label="卡片视图"
            @click="emit('update:view-mode', 'comfortable')"
          >
            <LayoutList :size="13" />
          </button>
          <button
            type="button"
            class="view-toggle-btn"
            :class="{ active: viewMode === 'compact' }"
            title="紧凑列表"
            aria-label="紧凑列表"
            @click="emit('update:view-mode', 'compact')"
          >
            <List :size="13" />
          </button>
        </div>

        <button
          v-if="activeQueueCount > 0"
          class="btn-secondary q-action-btn q-select-toggle"
          :class="{ active: selectionMode }"
          @click="toggleSelectionMode"
        >
          <ListChecks :size="13" />
          <span>{{ selectionMode ? '退出多选' : '多选' }}</span>
        </button>
        <button class="btn-secondary q-action-btn" :disabled="batchBusy" @click="emit('pause-all')">
          <Pause :size="13" />
          <span>全部暂停</span>
        </button>
        <button class="btn-secondary q-action-btn" :disabled="batchBusy" @click="emit('resume-all')">
          <Play :size="13" />
          <span>全部继续</span>
        </button>
      </div>
    </div>

    <!-- Contextual Batch Action Bar -->
    <div v-if="selectionMode && filteredTasks.length > 0" class="batch-bar">
      <div class="batch-left">
        <button
          type="button"
          class="batch-select-all"
          @click="toggleSelectAllVisible"
        >
          <span
            class="queue-check-box"
            :class="{ checked: selectionSummary.allSelected, mixed: selectionSummary.someSelected }"
            aria-hidden="true"
          >
            <Check v-if="selectionSummary.allSelected" :size="12" :stroke-width="3" />
            <Minus v-else-if="selectionSummary.someSelected" :size="12" :stroke-width="3" />
          </span>
          <span>全选</span>
        </button>

        <button
          type="button"
          class="btn-secondary batch-chip-btn"
          @click="invertSelection"
        >
          反选
        </button>

        <span class="batch-count">
          已选 <strong>{{ selectionSummary.selectedCount }}</strong> / {{ selectionSummary.totalCount }} 项
        </span>
      </div>

      <div class="batch-right">
        <button
          type="button"
          class="btn-secondary batch-action-btn batch-resume-btn"
          :disabled="batchBusy || selectionSummary.resumableIds.length === 0"
          @click="triggerBatchResume"
        >
          <Play :size="13" />
          <span>继续{{ selectionSummary.resumableIds.length > 0 ? ` (${selectionSummary.resumableIds.length})` : '' }}</span>
        </button>

        <button
          type="button"
          class="btn-secondary batch-action-btn batch-pause-btn"
          :disabled="batchBusy || selectionSummary.pausableIds.length === 0"
          @click="triggerBatchPause"
        >
          <Pause :size="13" />
          <span>暂停{{ selectionSummary.pausableIds.length > 0 ? ` (${selectionSummary.pausableIds.length})` : '' }}</span>
        </button>

        <button
          type="button"
          class="btn-secondary batch-action-btn batch-delete-btn"
          :disabled="batchBusy || selectionSummary.deletableIds.length === 0"
          @click="openBatchDeleteModal"
        >
          <Trash2 :size="13" />
          <span>删除{{ selectionSummary.deletableIds.length > 0 ? ` (${selectionSummary.deletableIds.length})` : '' }}</span>
        </button>
      </div>
    </div>

    <!-- Task List -->
    <div v-if="filteredTasks.length > 0" class="task-list" :class="{ 'is-compact': viewMode === 'compact' }">
      <div
        v-for="task in filteredTasks"
        :key="task.id"
        class="task-card"
        :class="[
          'status-' + task.status,
          { 'is-compact': viewMode === 'compact', 'is-selectable': selectionMode, 'is-selected': isTaskSelected(task.id) }
        ]"
        @click="handleTaskCardClick(task.id, $event)"
      >
        <button
          v-if="selectionMode"
          type="button"
          class="queue-check-btn"
          role="checkbox"
          :aria-checked="isTaskSelected(task.id)"
          :aria-label="`选择任务 ${task.partTitle || task.title}`"
          @click.stop="handleTaskCheckClick(task.id, $event)"
        >
          <span class="queue-check-box" :class="{ checked: isTaskSelected(task.id) }">
            <Check v-if="isTaskSelected(task.id)" :size="12" :stroke-width="3" />
          </span>
        </button>

        <div v-if="viewMode !== 'compact'" class="task-cover-box">
          <img
            v-if="task.cover"
            :src="normalizeImg(task.cover)"
            class="task-cover"
            referrerpolicy="no-referrer"
            alt="Cover"
          />
          <div v-else class="task-cover-placeholder">
            <Film :size="22" />
          </div>
          <span v-if="task.qualityLabel" class="quality-badge">{{ task.qualityLabel }}</span>
        </div>

        <div class="task-info">
          <div class="task-title-col">
            <div class="task-title-row">
              <span class="task-title" :title="task.partTitle || task.title">{{ (task as any).partCount > 1 ? `P${(task as any).partPage} · ` : '' }}{{ task.partTitle || task.title }}</span>
              <span v-if="viewMode !== 'compact' && task.partTitle && task.partTitle !== task.title" class="part-badge" :title="(task as any).videoTitle && (task as any).videoTitle !== task.partTitle ? `${task.title} › ${(task as any).videoTitle}` : task.title">
                {{ (task as any).videoTitle && (task as any).videoTitle !== task.partTitle ? (task as any).videoTitle : task.title }}
              </span>
            </div>
            <div v-if="viewMode === 'compact' && (task.qualityLabel || (task.partTitle && task.partTitle !== task.title))" class="compact-subline">
              <span v-if="task.qualityLabel" class="compact-quality">{{ task.qualityLabel }}</span>
              <span
                v-if="task.partTitle && task.partTitle !== task.title"
                class="compact-collection"
                :title="(task as any).videoTitle && (task as any).videoTitle !== task.partTitle ? `${task.title} › ${(task as any).videoTitle}` : task.title"
              >
                {{ (task as any).videoTitle && (task as any).videoTitle !== task.partTitle ? (task as any).videoTitle : task.title }}
              </span>
            </div>
          </div>

          <!-- Progress Bar -->
          <div class="progress-wrap">
            <div class="progress-bar">
              <div
                class="progress-fill"
                :class="'fill-' + task.status"
                :style="{ width: `${task.progress || 0}%` }"
              ></div>
            </div>
          </div>

          <!-- Metrics Row -->
          <div class="metrics-row">
            <div class="metrics-left">
              <!-- Status Tag -->
              <span v-if="task.status === 'downloading'" class="status-tag tag-downloading">
                <span class="pulse-dot"></span>
                <span>下载中 {{ task.speedStr }}</span>
              </span>
              <span v-else-if="task.status === 'merging'" class="status-tag tag-merging">
                <span class="pulse-dot-purple"></span>
                <span>音视频合成中...</span>
              </span>
              <span v-else-if="task.status === 'paused'" class="status-tag tag-paused">
                <span>已暂停</span>
              </span>
              <span v-else-if="task.status === 'queued'" class="status-tag tag-queued">
                <span>排队中</span>
              </span>
              <span v-else-if="task.status === 'error'" class="status-tag tag-error" :title="task.errorMsg">
                <AlertCircle :size="12" />
                <span>{{ isLegacyPlaybackError(task) ? '暂时无法获取这个视频的下载地址' : (task.errorMsg || '下载失败') }}</span>
              </span>
              <span v-else-if="task.status === 'cancelled'" class="status-tag tag-cancelled">
                <span>已取消</span>
              </span>

              <!-- Transferred Size -->
              <span v-if="task.sizeStr && task.status !== 'error'" class="metric-text size-metric">
                <HardDrive :size="11" />
                <span>{{ task.sizeStr }}</span>
              </span>

              <!-- ETA -->
              <span v-if="task.status === 'downloading' && task.etaStr" class="metric-text eta-metric">
                <Clock :size="11" />
                <span>剩余 {{ task.etaStr }}</span>
              </span>
            </div>

            <div class="metrics-right">
              <span class="progress-percent">{{ (task.progress || 0).toFixed(1) }}%</span>
            </div>
          </div>
          <div v-if="task.status === 'error' && (task.errorKind || isLegacyPlaybackError(task))" class="task-error-help">
            <p>{{ task.errorHint || '请稍后重试，或在B站检查是否能完整播放。' }}</p>
            <div class="error-help-actions">
              <button v-if="taskSourceURL(task)" @click="BrowserOpenURL(taskSourceURL(task))"><ExternalLink :size="12" />在B站打开</button>
              <button v-if="isPermissionError(task.errorKind)" @click="emit('open-login')">登录 / 重新登录</button>
            </div>
            <details v-if="task.errorDetail || isLegacyPlaybackError(task)"><summary>详细信息</summary>{{ task.errorDetail || task.errorMsg }}</details>
          </div>
        </div>

        <!-- Task Actions -->
        <div class="task-btn-group">
          <button
            v-if="task.status === 'downloading' || task.status === 'queued'"
            class="btn-icon task-btn"
            @click="emit('pause-task', task.id)"
            :title="task.status === 'queued' ? '暂停排队' : '暂停'"
          >
            <Pause :size="15" />
          </button>

          <button
            v-else-if="task.status === 'paused'"
            class="btn-icon task-btn"
            @click="emit('resume-task', task.id)"
            title="继续"
          >
            <Play :size="15" />
          </button>

          <button
            v-else-if="task.status === 'error' || task.status === 'cancelled'"
            class="btn-icon task-btn"
            @click="emit('resume-task', task.id)"
            title="重试"
          >
            <RotateCcw :size="15" />
          </button>

          <button
            class="btn-icon task-btn"
            @click="emit('open-dir', task.outputPath)"
            title="在文件夹中显示"
          >
            <FolderOpen :size="15" />
          </button>

          <button
            class="btn-icon task-btn delete-btn"
            @click="emit('delete-task', task.id, false)"
            title="清除记录"
          >
            <Trash2 :size="15" />
          </button>
        </div>
      </div>
    </div>

    <!-- Empty Queue State -->
    <div v-else class="empty-queue">
      <div class="empty-icon-box">
        <Zap :size="32" />
      </div>
      <h3 class="empty-title">当前没有下载任务</h3>
      <p class="empty-desc">在「解析下载」中输入视频链接并选择分集开始下载吧</p>
    </div>

    <!-- Batch Delete Confirmation Modal -->
    <div v-if="showBatchDeleteModal" class="modal-overlay" @click.self="showBatchDeleteModal = false">
      <div class="modal-card">
        <div class="modal-header">
          <div class="modal-title-box">
            <AlertTriangle :size="18" class="modal-icon-danger" />
            <h3 class="modal-title">删除选中的下载任务</h3>
          </div>
          <button class="btn-close" @click="showBatchDeleteModal = false">
            <X :size="16" />
          </button>
        </div>

        <div class="modal-body">
          <p class="modal-desc">
            确定要从下载队列中删除选中的 <strong>{{ selectionSummary.deletableIds.length }}</strong> 个任务吗？未完成的临时缓存文件也会一并清理。
          </p>

          <div class="batch-preview-list">
            <div v-for="task in batchDeletePreviewTasks" :key="task.id" class="batch-preview-item">
              <span class="batch-preview-title">{{ task.partTitle || task.title }}</span>
            </div>
            <div v-if="selectionSummary.deletableIds.length > batchDeletePreviewTasks.length" class="batch-preview-more">
              以及其余 {{ selectionSummary.deletableIds.length - batchDeletePreviewTasks.length }} 个任务
            </div>
          </div>
        </div>

        <div class="modal-footer">
          <button class="btn-secondary" @click="showBatchDeleteModal = false">
            取消
          </button>
          <button class="btn-danger" :disabled="batchBusy" @click="confirmBatchDelete">
            确认删除 ({{ selectionSummary.deletableIds.length }})
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.queue-container {
  display: flex;
  flex-direction: column;
  gap: 16px;
  width: 100%;
  max-width: 920px;
  margin: 0 auto;
}

.queue-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
  padding-bottom: 4px;
}

.filter-tabs {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 3px;
  background: var(--segmented-track-bg);
  border: 1px solid var(--border-hairline);
  border-radius: 10px;
  flex-wrap: wrap;
}

.tab-btn {
  padding: 5px 11px;
  font-size: 12px;
  background: transparent;
  color: var(--text-muted);
  border-radius: 7px;
  transition: background var(--transition-fast), color var(--transition-fast), box-shadow var(--transition-fast);
}

.tab-btn:hover:not(.active) {
  color: var(--text-primary);
}

.tab-btn.active {
  background: var(--segmented-active-bg);
  color: var(--text-primary);
  font-weight: 600;
  box-shadow: var(--segmented-active-shadow);
}

.tab-btn-error.active {
  background: rgba(239, 68, 68, 0.16);
  color: var(--danger);
  box-shadow: none;
}

.queue-actions {
  display: flex;
  align-items: center;
  gap: 6px;
}

.view-toggle {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 2px;
  height: 28px;
  border-radius: 8px;
  background: var(--segmented-track-bg);
  border: 1px solid var(--border-hairline);
}

.view-toggle-btn {
  width: 24px;
  height: 22px;
  padding: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--text-muted);
}

.view-toggle-btn:hover:not(.active) {
  color: var(--text-primary);
}

.view-toggle-btn.active {
  background: var(--segmented-active-bg);
  color: var(--text-primary);
  box-shadow: var(--segmented-active-shadow);
}

.q-action-btn {
  height: 28px;
  font-size: 11.5px;
  padding: 0 8px;
  gap: 4px;
}

.q-action-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.q-select-toggle.active {
  background: rgba(251, 114, 153, 0.14);
  border-color: rgba(251, 114, 153, 0.38);
  color: var(--bili-pink);
}

/* Batch Action Bar */
.batch-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
  padding: 8px 12px;
  background: var(--bg-card);
  border: 1px solid rgba(251, 114, 153, 0.28);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-sm);
}

.batch-left {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.batch-select-all {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  height: 28px;
  padding: 0 6px 0 2px;
  background: transparent;
  color: var(--text-primary);
  font-size: 12px;
  font-weight: 500;
  border-radius: var(--radius-xs);
}

.batch-select-all:hover {
  background: var(--neutral-05);
}

.batch-chip-btn {
  height: 26px;
  padding: 0 9px;
  font-size: 11.5px;
  border-radius: var(--radius-xs);
}

.batch-count {
  font-size: 12px;
  color: var(--text-secondary);
}

.batch-count strong {
  color: var(--bili-pink);
  font-weight: 600;
}

.batch-right {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.batch-action-btn {
  height: 28px;
  padding: 0 10px;
  font-size: 11.5px;
  gap: 4px;
}

.batch-action-btn:disabled {
  opacity: 0.42;
  cursor: not-allowed;
}

.batch-resume-btn:not(:disabled):hover {
  border-color: rgba(251, 114, 153, 0.4);
  color: var(--bili-pink);
}

.batch-pause-btn:not(:disabled):hover {
  border-color: rgba(245, 158, 11, 0.4);
  color: var(--warning);
}

.batch-delete-btn:not(:disabled) {
  color: var(--danger);
  border-color: rgba(239, 68, 68, 0.25);
  background: rgba(239, 68, 68, 0.08);
}

.batch-delete-btn:not(:disabled):hover {
  background: rgba(239, 68, 68, 0.16);
  border-color: rgba(239, 68, 68, 0.45);
}

.queue-check-btn {
  display: grid;
  place-items: center;
  padding: 2px;
  background: transparent;
  flex-shrink: 0;
  border-radius: var(--radius-xs);
}

.queue-check-box {
  display: grid;
  place-items: center;
  width: 18px;
  height: 18px;
  border: 1.5px solid var(--neutral-25);
  border-radius: 5px;
  background: transparent;
  color: var(--bg-app);
  transition: all var(--transition-fast);
}

.queue-check-box.checked,
.queue-check-box.mixed {
  border-color: var(--bili-pink);
  background: var(--bili-pink);
  color: var(--bg-app);
}

.task-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.task-card {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 12px 14px;
  background: var(--bg-card);
  border: 1px solid var(--border-hairline);
  border-radius: var(--radius-md);
  box-shadow: var(--surface-highlight), var(--shadow-sm);
  transition: background var(--transition-fast);
}

.task-card:hover {
  background: var(--bg-card-hover);
  border-color: var(--border-subtle);
}

.task-card.is-selectable {
  cursor: pointer;
}

.task-card.is-selected {
  background: var(--episode-selected-bg);
  border-color: var(--border-hover);
}

.task-list.is-compact {
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 12px;
  gap: 0;
  overflow: hidden;
  box-shadow: var(--surface-highlight), var(--shadow-sm);
}

.task-card.is-compact {
  padding: 8px 14px;
  gap: 12px;
  background: transparent;
  border: 0 solid transparent;
  border-radius: 0;
  box-shadow: none;
}

.task-card.is-compact + .task-card.is-compact {
  border-top: 1px solid var(--border-hairline);
}

.task-card.is-compact:hover {
  background: var(--bg-card-hover);
}

.task-card.is-compact.is-selected {
  background: var(--episode-selected-bg);
}

.task-title-col {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.task-card.is-compact:not(.status-error) .task-info {
  flex-direction: row;
  align-items: center;
  gap: 12px;
}

.task-card.is-compact:not(.status-error) .task-title-col {
  flex: 1;
  min-width: 0;
}

.task-card.is-compact:not(.status-error) .progress-wrap {
  width: 72px;
  flex-shrink: 0;
}

.task-card.is-compact:not(.status-error) .metrics-row {
  flex-shrink: 0;
  gap: 8px;
  justify-content: flex-end;
}

.task-card.is-compact:not(.status-error) .metrics-left {
  flex-wrap: nowrap;
  gap: 8px;
}

.task-card.is-compact:not(.status-error) .status-tag {
  width: 112px;
  justify-content: flex-end;
  white-space: nowrap;
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
}

.task-card.is-compact:not(.status-error) .size-metric {
  width: 126px;
  justify-content: flex-end;
  white-space: nowrap;
  flex-shrink: 0;
}

.task-card.is-compact:not(.status-error) .metrics-right {
  width: 44px;
  text-align: right;
  flex-shrink: 0;
}

.task-card.is-compact .eta-metric,
.task-card.is-compact .size-metric svg {
  display: none;
}

.task-card.is-compact .task-btn {
  width: 26px;
  height: 26px;
}

.compact-subline {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.compact-collection {
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.compact-quality {
  font-size: 10px;
  font-weight: 600;
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--neutral-06);
  color: var(--text-secondary);
  white-space: nowrap;
  flex-shrink: 0;
}

.task-cover-box {
  position: relative;
  width: 96px;
  height: 60px;
  border-radius: var(--radius-sm);
  overflow: hidden;
  flex-shrink: 0;
  background: rgba(0, 0, 0, 0.35);
  box-shadow: var(--cover-ring);
}

.task-cover {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.task-cover-placeholder {
  width: 100%;
  height: 100%;
  background: var(--neutral-05);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
}

.quality-badge {
  position: absolute;
  bottom: 3px;
  right: 3px;
  background: rgba(0, 0, 0, 0.75);
  backdrop-filter: blur(4px);
  color: #fff;
  font-size: 9px;
  font-weight: 600;
  padding: 1px 4px;
  border-radius: var(--radius-xs);
}

.task-info {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 6px;
  overflow: hidden;
  min-width: 0;
}

.task-title-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.task-title {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.part-badge {
  font-size: 10.5px;
  color: var(--text-secondary);
  background: var(--neutral-06);
  padding: 1px 6px;
  border-radius: var(--radius-xs);
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex-shrink: 0;
}

.progress-wrap {
  width: 100%;
}

.progress-bar {
  width: 100%;
  height: 3.5px;
  background: var(--neutral-08);
  border-radius: var(--radius-full);
  overflow: hidden;
}

.progress-fill {
  height: 100%;
  background: linear-gradient(90deg, var(--bili-pink), #ff7099);
  border-radius: var(--radius-full);
  transition: width 0.25s ease-out;
}

.fill-merging {
  background: linear-gradient(90deg, #8b5cf6, #a855f7);
}

.fill-paused {
  background: var(--warning);
}

.fill-error {
  background: var(--danger);
}

.metrics-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 11px;
}

.metrics-left {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  overflow: hidden;
}

.task-error-help { color: var(--text-muted); font-size: 11.5px; line-height: 1.5; }
.error-help-actions { display: flex; gap: 14px; margin-top: 5px; }
.error-help-actions button { color: var(--bili-pink); display: inline-flex; gap: 4px; align-items: center; font-size: 11.5px; background: transparent; }
.task-error-help details { margin-top: 5px; font-size: 10.5px; }
.task-error-help summary { cursor: pointer; }
.status-error .metrics-left { overflow: visible; }
.status-error .status-tag { align-items: flex-start; }
.status-error .status-tag svg { flex-shrink: 0; margin-top: 2px; }

.status-tag {
  display: flex;
  align-items: center;
  gap: 4px;
  font-weight: 600;
}

.tag-downloading {
  color: var(--bili-pink);
}

.pulse-dot {
  width: 5px;
  height: 5px;
  border-radius: var(--radius-full);
  background: var(--bili-pink);
  box-shadow: 0 0 6px var(--bili-pink);
  animation: pulseGlow 1.5s infinite;
}

.tag-merging {
  color: #a855f7;
}

.pulse-dot-purple {
  width: 5px;
  height: 5px;
  border-radius: var(--radius-full);
  background: #a855f7;
  box-shadow: 0 0 6px #a855f7;
  animation: pulseGlow 1.5s infinite;
}

.tag-paused {
  color: var(--warning);
}

.tag-queued {
  color: var(--text-muted);
}

.tag-error {
  color: var(--danger);
}

.tag-cancelled {
  color: var(--text-muted);
}

.metric-text {
  display: flex;
  align-items: center;
  gap: 4px;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
}

.progress-percent {
  font-weight: 600;
  color: var(--text-secondary);
  font-variant-numeric: tabular-nums;
}

.task-btn-group {
  display: flex;
  align-items: center;
  gap: 5px;
  flex-shrink: 0;
}

.task-btn {
  width: 30px;
  height: 30px;
}

.delete-btn:hover {
  color: var(--danger);
}

/* Empty State */
.empty-queue {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 50px 20px;
  text-align: center;
}

.empty-icon-box {
  width: 56px;
  height: 56px;
  border-radius: var(--radius-md);
  background: var(--neutral-04);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  margin-bottom: 12px;
}

.empty-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
  margin-bottom: 4px;
}

.empty-desc {
  font-size: 12.5px;
  color: var(--text-muted);
}

/* Batch Delete Modal */
.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(6, 8, 12, 0.72);
  backdrop-filter: blur(10px);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  animation: fadeIn 0.2s ease-out;
}

.modal-card {
  background: var(--bg-modal);
  border: 1px solid var(--border-popover);
  border-radius: 16px;
  box-shadow: var(--shadow-lg);
  width: 90%;
  max-width: 440px;
  overflow: hidden;
  animation: scaleIn 0.2s cubic-bezier(0.16, 1, 0.3, 1);
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border-subtle);
}

.modal-title-box {
  display: flex;
  align-items: center;
  gap: 8px;
}

.modal-icon-danger {
  color: var(--danger);
}

.modal-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0;
}

.btn-close {
  background: transparent;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  padding: 4px;
  border-radius: var(--radius-xs);
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all var(--transition-fast);
}

.btn-close:hover {
  color: var(--text-primary);
  background: var(--neutral-1);
}

.modal-body {
  padding: 18px 20px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.modal-desc {
  font-size: 13.5px;
  color: var(--text-secondary);
  line-height: 1.5;
  margin: 0;
}

.batch-preview-list {
  background: var(--neutral-03);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.batch-preview-item {
  display: flex;
  align-items: center;
  min-width: 0;
}

.batch-preview-title {
  font-size: 12.5px;
  font-weight: 500;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.batch-preview-more {
  font-size: 11.5px;
  color: var(--text-muted);
}

.modal-footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  padding: 14px 20px;
  border-top: 1px solid var(--border-subtle);
  background: var(--neutral-02);
}

.btn-danger {
  background: var(--danger);
  color: #fff;
  border: none;
  border-radius: var(--radius-sm);
  padding: 0 14px;
  height: 32px;
  font-size: 12.5px;
  font-weight: 500;
  cursor: pointer;
  transition: all var(--transition-fast);
}

.btn-danger:hover:not(:disabled) {
  filter: brightness(1.12);
  box-shadow: 0 2px 8px var(--danger-glow);
}

.btn-danger:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
