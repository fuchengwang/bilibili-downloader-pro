<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { CheckHistoryFiles, RemoveMissingHistoryRecords } from '../../wailsjs/go/main/App'
import {
  Play,
  FolderOpen,
  Trash2,
  CheckCircle2,
  Search,
  HardDrive,
  Clock,
  Film,
  FileX2,
  AlertTriangle,
  X,
  ListChecks,
  Check,
  Minus,
  LayoutList,
  List
} from 'lucide-vue-next'
import { downloader } from '../../wailsjs/go/models'
import {
  formatHistoryTotalSize,
  pruneSelectedHistoryIds,
  summarizeQueueSelection,
  toggleQueueTaskSelection
} from '../utils/task-queue'

const props = withDefaults(defineProps<{
  tasks: downloader.DownloadTask[]
  active: boolean
  batchBusy?: boolean
}>(), {
  batchBusy: false
})

const emit = defineEmits<{
  (e: 'open-file', path: string, task?: downloader.DownloadTask): void
  (e: 'open-dir', path: string): void
  (e: 'delete-task', id: string, deleteFile: boolean): void
  (e: 'request-delete', task: downloader.DownloadTask): void
  (e: 'clear-completed', deleteFiles?: boolean): void
  (e: 'batch-delete', ids: string[], deleteFiles: boolean): void
  (e: 'records-removed', ids: string[]): void
}>()

const searchKey = ref('')
const fileStates = ref<Record<string, string>>({})
const checking = ref(false)
const cleaning = ref(false)
const cleanupError = ref('')
let checkVersion = 0

function readSavedViewMode(): 'comfortable' | 'compact' {
  try {
    return typeof localStorage !== 'undefined' && localStorage.getItem('bbdown-history-view') === 'compact'
      ? 'compact'
      : 'comfortable'
  } catch {
    return 'comfortable'
  }
}

const viewMode = ref<'comfortable' | 'compact'>(readSavedViewMode())

function setViewMode(mode: 'comfortable' | 'compact') {
  viewMode.value = mode
  try {
    if (typeof localStorage !== 'undefined') localStorage.setItem('bbdown-history-view', mode)
  } catch {
    // ignore storage errors
  }
}

const selectionMode = ref(false)
const selectedIds = ref<Set<string>>(new Set())
const lastAnchorId = ref<string | null>(null)
const showBatchDeleteModal = ref(false)
const batchDeleteLocalFiles = ref(false)

const allCompleted = computed(() => props.tasks.filter(t => t.status === 'completed'))
const missingTasks = computed(() => allCompleted.value.filter(t => t.outputPath && fileStates.value[t.outputPath] === 'missing'))

async function checkFiles() {
  const version = ++checkVersion
  const paths = [...new Set(allCompleted.value.map(t => t.outputPath).filter(Boolean))]
  checking.value = true
  try {
    const states = await CheckHistoryFiles(paths)
    if (version === checkVersion) fileStates.value = states || {}
  } catch {
    if (version === checkVersion) fileStates.value = {}
  } finally {
    if (version === checkVersion) checking.value = false
  }
}
watch(() => props.active ? JSON.stringify(allCompleted.value.map(t => [t.id, t.outputPath])) : '', () => {
  if (props.active) void checkFiles()
  else { checkVersion++; checking.value = false }
}, { immediate: true })

async function confirmMissingCleanup() {
  cleaning.value = true
  cleanupError.value = ''
  const candidates = missingTasks.value.map(t => ({ id: t.id, path: t.outputPath }))
  let removed: string[] = []
  try {
    removed = await RemoveMissingHistoryRecords(Object.fromEntries(candidates.map(t => [t.id, t.path]))) || []
  } catch {
    cleanupError.value = '清理未完成，请稍后重试。'
  } finally {
    if (removed.length) emit('records-removed', removed)
    cleaning.value = false
    void checkFiles()
  }
}

// 单项删除弹窗状态
const showDeleteModal = ref(false)
const targetTask = ref<downloader.DownloadTask | null>(null)
const deleteLocalFile = ref(false)

// 批量清空弹窗状态
const showClearAllModal = ref(false)
const clearAllDeleteFiles = ref(false)

function normalizeImg(url?: string) {
  if (!url) return ''
  if (url.startsWith('//')) return 'https:' + url
  if (url.startsWith('http://')) return 'https://' + url.substring(7)
  return url
}

const completedTasks = computed(() => {
  const list = props.tasks.filter(t => t.status === 'completed')
  if (!searchKey.value.trim()) return list
  const kw = searchKey.value.toLowerCase().trim()
  return list.filter(t => (t.title || '').toLowerCase().includes(kw) || (t.partTitle || '').toLowerCase().includes(kw) || ((t as any).videoTitle || '').toLowerCase().includes(kw))
})

const totalSizeText = computed(() => formatHistoryTotalSize(completedTasks.value))
const selectionSummary = computed(() => summarizeQueueSelection(completedTasks.value, selectedIds.value))
const batchDeletePreviewTasks = computed(() => selectionSummary.value.selectedTasks.slice(0, 4))

function isTaskSelected(id: string) {
  return selectedIds.value.has(id)
}

function exitSelectionMode() {
  selectionMode.value = false
  selectedIds.value = new Set()
  lastAnchorId.value = null
  showBatchDeleteModal.value = false
  batchDeleteLocalFiles.value = false
}

function toggleSelectionMode() {
  if (selectionMode.value) exitSelectionMode()
  else selectionMode.value = true
}

function applySelection(taskId: string, shiftKey: boolean) {
  const visibleIds = completedTasks.value.map(t => t.id)
  const next = toggleQueueTaskSelection(selectedIds.value, visibleIds, taskId, {
    shiftKey,
    anchorId: lastAnchorId.value
  })
  selectedIds.value = next.selected
  lastAnchorId.value = next.anchorId
}

function handleCheckClick(taskId: string, event: MouseEvent) {
  applySelection(taskId, event.shiftKey)
}

function handleCardClick(taskId: string, event: MouseEvent) {
  const target = event.target as HTMLElement | null
  if (target?.closest('.btn-group, .cover-box, button, a')) return
  const shortcutSelect = event.metaKey || event.ctrlKey || event.shiftKey
  if (!selectionMode.value && !shortcutSelect) return
  if (!selectionMode.value) selectionMode.value = true
  applySelection(taskId, event.shiftKey)
}

function selectAllVisible() {
  const next = new Set(selectedIds.value)
  for (const task of completedTasks.value) {
    next.add(task.id)
  }
  selectedIds.value = next
  lastAnchorId.value = completedTasks.value[completedTasks.value.length - 1]?.id ?? null
}

function toggleSelectAllVisible() {
  const next = new Set(selectedIds.value)
  if (selectionSummary.value.allSelected) {
    for (const task of completedTasks.value) {
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
  for (const task of completedTasks.value) {
    if (next.has(task.id)) next.delete(task.id)
    else next.add(task.id)
  }
  selectedIds.value = next
  lastAnchorId.value = null
}

function openBatchDeleteModal() {
  if (props.batchBusy || selectionSummary.value.deletableIds.length === 0) return
  batchDeleteLocalFiles.value = false
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
  const deleteFiles = batchDeleteLocalFiles.value
  showBatchDeleteModal.value = false
  emit('batch-delete', ids, deleteFiles)
}

watch(() => props.tasks, (nextTasks) => {
  if (selectedIds.value.size > 0) {
    selectedIds.value = pruneSelectedHistoryIds(selectedIds.value, nextTasks)
  }
  if (!nextTasks.some(t => t.status === 'completed')) {
    exitSelectionMode()
  }
}, { deep: true })

function handleKeydown(event: KeyboardEvent) {
  if (!props.active || !selectionMode.value) return
  const target = event.target as HTMLElement | null
  if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return
  if (event.key === 'Escape') {
    event.preventDefault()
    if (showBatchDeleteModal.value) showBatchDeleteModal.value = false
    else exitSelectionMode()
    return
  }
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'a' && completedTasks.value.length > 0) {
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

function formatCompletedDate(timestamp: number): string {
  if (!timestamp) return ''
  const date = new Date(timestamp * 1000)
  return `${date.getMonth() + 1}月${date.getDate()}日 ${date.getHours().toString().padStart(2, '0')}:${date.getMinutes().toString().padStart(2, '0')}`
}

function openDeleteModal(task: downloader.DownloadTask) {
  targetTask.value = task
  deleteLocalFile.value = false
  showDeleteModal.value = true
}

function confirmDeleteSingle() {
  if (!targetTask.value) return
  emit('delete-task', targetTask.value.id, deleteLocalFile.value)
  showDeleteModal.value = false
  targetTask.value = null
}

function openClearAllModal() {
  clearAllDeleteFiles.value = false
  showClearAllModal.value = true
}

function confirmClearAll() {
  emit('clear-completed', clearAllDeleteFiles.value)
  showClearAllModal.value = false
}
</script>

<template>
  <div class="history-container">
    <!-- History Header -->
    <div class="history-header">
      <div class="search-box">
        <Search :size="14" class="search-icon" />
        <input
          v-model="searchKey"
          type="text"
          class="history-search"
          placeholder="搜索已下载的视频..."
        />
      </div>

      <div class="header-actions">
        <span class="count-text">
          共 {{ completedTasks.length }} 个文件<template v-if="totalSizeText"> · {{ totalSizeText }}</template>
        </span>

        <div v-if="completedTasks.length > 0" class="view-toggle" role="group" aria-label="视图切换">
          <button
            type="button"
            class="view-toggle-btn"
            :class="{ active: viewMode === 'comfortable' }"
            title="卡片视图"
            aria-label="卡片视图"
            @click="setViewMode('comfortable')"
          >
            <LayoutList :size="13" />
          </button>
          <button
            type="button"
            class="view-toggle-btn"
            :class="{ active: viewMode === 'compact' }"
            title="紧凑列表"
            aria-label="紧凑列表"
            @click="setViewMode('compact')"
          >
            <List :size="13" />
          </button>
        </div>

        <button
          v-if="completedTasks.length > 0"
          class="btn-secondary q-action-btn q-select-toggle"
          :class="{ active: selectionMode }"
          @click="toggleSelectionMode"
        >
          <ListChecks :size="13" />
          <span>{{ selectionMode ? '退出多选' : '多选' }}</span>
        </button>

        <button v-if="missingTasks.length" class="btn-secondary clear-btn missing-clear-btn" :disabled="checking || cleaning" @click="confirmMissingCleanup" :title="cleanupError || '仅清除原位置未找到文件的记录'">
          {{ cleaning ? '正在检查…' : `清理失效记录（${missingTasks.length}）` }}
        </button>
        <button
          v-if="completedTasks.length > 0"
          class="btn-secondary clear-btn"
          @click="openClearAllModal"
        >
          <Trash2 :size="13" />
          <span>清空记录</span>
        </button>
      </div>
    </div>

    <!-- Contextual Batch Action Bar -->
    <div v-if="selectionMode && completedTasks.length > 0" class="batch-bar">
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
          class="btn-secondary batch-action-btn batch-delete-btn"
          :disabled="batchBusy || selectionSummary.deletableIds.length === 0"
          @click="openBatchDeleteModal"
        >
          <Trash2 :size="13" />
          <span>删除{{ selectionSummary.deletableIds.length > 0 ? ` (${selectionSummary.deletableIds.length})` : '' }}</span>
        </button>
      </div>
    </div>

    <!-- History Task List -->
    <div v-if="completedTasks.length > 0" class="history-list" :class="{ 'is-compact': viewMode === 'compact' }">
      <div
        v-for="task in completedTasks"
        :key="task.id"
        class="history-card"
        :class="{
          'is-missing': fileStates[task.outputPath] === 'missing',
          'is-compact': viewMode === 'compact',
          'is-selectable': selectionMode,
          'is-selected': isTaskSelected(task.id)
        }"
        :title="fileStates[task.outputPath] === 'missing' ? '原位置未找到文件' : ''"
        @click="handleCardClick(task.id, $event)"
      >
        <button
          v-if="selectionMode"
          type="button"
          class="queue-check-btn"
          role="checkbox"
          :aria-checked="isTaskSelected(task.id)"
          :aria-label="`选择记录 ${task.partTitle || task.title}`"
          @click.stop="handleCheckClick(task.id, $event)"
        >
          <span class="queue-check-box" :class="{ checked: isTaskSelected(task.id) }">
            <Check v-if="isTaskSelected(task.id)" :size="12" :stroke-width="3" />
          </span>
        </button>

        <div v-if="viewMode !== 'compact'" class="cover-box" @click="fileStates[task.outputPath] !== 'missing' && emit('open-file', task.outputPath, task)">
          <img
            v-if="task.cover"
            :src="normalizeImg(task.cover)"
            class="cover-img"
            referrerpolicy="no-referrer"
            alt="Cover"
          />
          <div v-else class="cover-placeholder">
            <Film :size="24" />
          </div>
          <div class="play-overlay">
            <Play :size="18" class="play-icon" />
          </div>
          <div v-if="fileStates[task.outputPath] === 'missing'" class="missing-cover-mark" aria-label="原位置未找到文件"><FileX2 :size="17" /></div>
          <span v-if="task.qualityLabel" class="quality-badge">{{ task.qualityLabel }}</span>
        </div>

        <div class="content-box">
          <div class="title-row">
            <span v-if="viewMode === 'compact' && task.qualityLabel" class="compact-quality">{{ task.qualityLabel }}</span>
            <span class="item-title" :title="task.partTitle || task.title">{{ (task as any).partCount > 1 ? `P${(task as any).partPage} · ` : '' }}{{ task.partTitle || task.title }}</span>
            <span v-if="task.partTitle && task.partTitle !== task.title" class="part-badge" :title="(task as any).videoTitle && (task as any).videoTitle !== task.partTitle ? `${task.title} › ${(task as any).videoTitle}` : task.title">
              {{ (task as any).videoTitle && (task as any).videoTitle !== task.partTitle ? (task as any).videoTitle : task.title }}
            </span>
          </div>

          <div class="meta-row">
            <span class="meta-item status-meta">
              <AlertTriangle v-if="fileStates[task.outputPath] === 'missing'" :size="12" class="missing-icon" aria-label="原位置未找到文件" />
              <CheckCircle2 v-else :size="12" class="success-icon" />
              <span>{{ fileStates[task.outputPath] === 'missing' ? '文件缺失' : '下载完成' }}</span>
            </span>

            <span v-if="task.sizeStr" class="meta-item size-meta">
              <HardDrive :size="11" />
              <span>{{ task.sizeStr.split('/')[0]?.trim() || task.sizeStr }}</span>
            </span>

            <span v-if="task.completedAt" class="meta-item date-meta">
              <Clock :size="11" />
              <span>{{ formatCompletedDate(task.completedAt) }}</span>
            </span>
          </div>
        </div>

        <div class="btn-group">
          <button
            v-if="viewMode === 'compact'"
            class="btn-icon compact-play-btn"
            :disabled="fileStates[task.outputPath] === 'missing'"
            @click="emit('open-file', task.outputPath, task)"
            :title="fileStates[task.outputPath] === 'missing' ? '原位置未找到文件' : '播放视频'"
          >
            <Play :size="13" />
          </button>
          <button
            v-else
            class="btn-primary play-btn"
            :disabled="fileStates[task.outputPath] === 'missing'"
            @click="emit('open-file', task.outputPath, task)"
            :title="fileStates[task.outputPath] === 'missing' ? '原位置未找到文件' : '播放视频'"
          >
            <Play :size="13" />
            <span>播放</span>
          </button>

          <button class="btn-icon dir-btn" @click="emit('open-dir', task.outputPath)" title="在访达/资源管理器中显示">
            <FolderOpen :size="14" />
          </button>

          <button class="btn-icon del-btn" @click="openDeleteModal(task)" title="删除任务/文件">
            <Trash2 :size="14" />
          </button>
        </div>
      </div>
    </div>

    <!-- Empty State -->
    <div v-else class="empty-history">
      <div class="empty-icon-box">
        <CheckCircle2 :size="32" />
      </div>
      <h3 class="empty-title">暂无下载完成记录</h3>
      <p class="empty-desc">下载完成后的视频将在这里展示，并支持一键播放与定位</p>
    </div>

    <p v-if="cleanupError" class="cleanup-error" role="alert">{{ cleanupError }}</p>

    <!-- 单任务删除确认模态框 -->
    <div v-if="showDeleteModal && targetTask" class="modal-overlay" @click.self="showDeleteModal = false">
      <div class="modal-card">
        <div class="modal-header">
          <div class="modal-title-box">
            <Trash2 :size="18" class="modal-icon-danger" />
            <h3 class="modal-title">删除已完成任务</h3>
          </div>
          <button class="btn-close" @click="showDeleteModal = false">
            <X :size="16" />
          </button>
        </div>

        <div class="modal-body">
          <p class="modal-desc">您确定要从下载历史中删除此条记录吗？</p>
          <div class="task-preview-box">
            <div class="preview-title">{{ targetTask.title }}</div>
            <div v-if="targetTask.partTitle && targetTask.partTitle !== targetTask.title" class="preview-part">
              {{ targetTask.partTitle }}
            </div>
            <div v-if="targetTask.outputPath" class="preview-path" :title="targetTask.outputPath">
              {{ targetTask.outputPath }}
            </div>
          </div>

          <label class="checkbox-label">
            <input
              v-model="deleteLocalFile"
              type="checkbox"
              class="custom-checkbox"
            />
            <div class="checkbox-text">
              <span class="checkbox-main">同时删除本地视频源文件</span>
              <span class="checkbox-sub">如勾选，该视频文件将永久从您的磁盘中删除</span>
            </div>
          </label>
        </div>

        <div class="modal-footer">
          <button class="btn-secondary" @click="showDeleteModal = false">
            取消
          </button>
          <button
            class="btn-danger"
            @click="confirmDeleteSingle"
          >
            {{ deleteLocalFile ? '删除记录与本地文件' : '仅清除记录' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 批量删除确认模态框 -->
    <div v-if="showBatchDeleteModal" class="modal-overlay" @click.self="showBatchDeleteModal = false">
      <div class="modal-card">
        <div class="modal-header">
          <div class="modal-title-box">
            <AlertTriangle :size="18" class="modal-icon-danger" />
            <h3 class="modal-title">删除选中的已完成记录</h3>
          </div>
          <button class="btn-close" @click="showBatchDeleteModal = false">
            <X :size="16" />
          </button>
        </div>

        <div class="modal-body">
          <p class="modal-desc">
            确定要从下载历史中删除选中的 <strong>{{ selectionSummary.deletableIds.length }}</strong> 条记录吗？
          </p>

          <div class="batch-preview-list">
            <div v-for="task in batchDeletePreviewTasks" :key="task.id" class="batch-preview-item">
              <span class="batch-preview-title">{{ task.partTitle || task.title }}</span>
            </div>
            <div v-if="selectionSummary.deletableIds.length > batchDeletePreviewTasks.length" class="batch-preview-more">
              以及其余 {{ selectionSummary.deletableIds.length - batchDeletePreviewTasks.length }} 条记录
            </div>
          </div>

          <label class="checkbox-label">
            <input
              v-model="batchDeleteLocalFiles"
              type="checkbox"
              class="custom-checkbox"
            />
            <div class="checkbox-text">
              <span class="checkbox-main danger-text">同时删除这 {{ selectionSummary.deletableIds.length }} 个本地视频源文件</span>
              <span class="checkbox-sub">注意：勾选后对应的本地视频文件将被永久删除！</span>
            </div>
          </label>
        </div>

        <div class="modal-footer">
          <button class="btn-secondary" @click="showBatchDeleteModal = false">
            取消
          </button>
          <button class="btn-danger" :disabled="batchBusy" @click="confirmBatchDelete">
            {{ batchDeleteLocalFiles ? `删除记录与文件 (${selectionSummary.deletableIds.length})` : `仅移除记录 (${selectionSummary.deletableIds.length})` }}
          </button>
        </div>
      </div>
    </div>

    <!-- 批量清空确认模态框 -->
    <div v-if="showClearAllModal" class="modal-overlay" @click.self="showClearAllModal = false">
      <div class="modal-card">
        <div class="modal-header">
          <div class="modal-title-box">
            <AlertTriangle :size="18" class="modal-icon-warning" />
            <h3 class="modal-title">清空下载完成记录</h3>
          </div>
          <button class="btn-close" @click="showClearAllModal = false">
            <X :size="16" />
          </button>
        </div>

        <div class="modal-body">
          <p class="modal-desc">
            确定要清空全部 <strong>{{ completedTasks.length }}</strong> 条已完成下载记录吗？
          </p>

          <label class="checkbox-label">
            <input
              v-model="clearAllDeleteFiles"
              type="checkbox"
              class="custom-checkbox"
            />
            <div class="checkbox-text">
              <span class="checkbox-main danger-text">同时删除这 {{ completedTasks.length }} 个本地视频文件</span>
              <span class="checkbox-sub">注意：勾选后所有已下载的本地视频源文件将被永久删除！</span>
            </div>
          </label>
        </div>

        <div class="modal-footer">
          <button class="btn-secondary" @click="showClearAllModal = false">
            取消
          </button>
          <button
            class="btn-danger"
            @click="confirmClearAll"
          >
            {{ clearAllDeleteFiles ? '清空记录并删除所有文件' : '仅清空记录' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.history-container {
  display: flex;
  flex-direction: column;
  gap: 16px;
  width: 100%;
  max-width: 920px;
  margin: 0 auto;
}

.history-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  padding-bottom: 4px;
}

.search-box {
  display: flex;
  align-items: center;
  gap: 7px;
  background: var(--bg-control);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  box-shadow: var(--surface-highlight);
  padding: 0 11px;
  width: 240px;
}

.search-icon {
  color: var(--text-muted);
}

.history-search {
  height: 30px;
  background: transparent;
  border: none;
  font-size: 12px;
  color: var(--text-primary);
  flex: 1;
}

.history-search:focus {
  border: none;
  box-shadow: none;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.count-text {
  font-size: 12px;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
  margin-right: 4px;
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

.q-action-btn,
.clear-btn {
  height: 28px;
  font-size: 11.5px;
  padding: 0 9px;
  gap: 4px;
}

.q-select-toggle.active {
  background: rgba(242, 93, 142, 0.14);
  border-color: rgba(242, 93, 142, 0.38);
  color: var(--bili-pink);
}

.missing-clear-btn { justify-content: center; padding-inline: 10px; text-align: center; }

/* Batch Action Bar */
.batch-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
  padding: 8px 12px;
  background: var(--bg-card);
  border: 1px solid rgba(242, 93, 142, 0.28);
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

.history-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.history-list.is-compact {
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 12px;
  gap: 0;
  overflow: hidden;
  box-shadow: var(--surface-highlight), var(--shadow-sm);
}

.history-card {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 12px 14px;
  background: var(--bg-card);
  border: 1px solid var(--border-hairline);
  border-radius: var(--radius-md);
  box-shadow: var(--surface-highlight), var(--shadow-sm);
  transition: background var(--transition-fast), border-color var(--transition-fast);
}

.history-card:hover {
  background: var(--bg-card-hover);
  border-color: var(--border-subtle);
}

.history-card.is-selectable {
  cursor: pointer;
}

.history-card.is-selected {
  background: var(--episode-selected-bg);
  border-color: var(--border-hover);
}

.history-card.is-compact {
  padding: 9px 14px;
  gap: 10px;
  background: transparent;
  border: none;
  border-radius: 0;
  box-shadow: none;
}

.history-card.is-compact + .history-card.is-compact {
  border-top: 1px solid var(--border-hairline);
}

.history-card.is-compact:hover {
  background: var(--bg-card-hover);
}

.history-card.is-compact.is-selected {
  background: var(--episode-selected-bg);
}

.history-card.is-compact .content-box {
  flex-direction: row;
  align-items: center;
  gap: 12px;
}

.history-card.is-compact .title-row {
  flex: 1;
  min-width: 0;
}

.history-card.is-compact .meta-row {
  flex-shrink: 0;
  gap: 10px;
}

.history-card.is-compact .status-meta {
  display: none;
}

.history-card.is-compact.is-missing .status-meta {
  display: flex;
  color: var(--warning);
}

.history-card.is-compact .size-meta svg,
.history-card.is-compact .date-meta svg {
  display: none;
}

.history-card.is-compact .dir-btn,
.history-card.is-compact .del-btn,
.history-card.is-compact .compact-play-btn {
  width: 26px;
  height: 26px;
}

.compact-quality {
  font-size: 10px;
  font-weight: 600;
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--neutral-08);
  color: var(--text-secondary);
  white-space: nowrap;
  flex-shrink: 0;
}

.history-card.is-missing .cover-box { cursor: default; }
.history-card.is-missing .content-box { opacity: .65; }
.missing-cover-mark { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: white; }
.missing-cover-mark svg { box-sizing: content-box; padding: 5px; border-radius: 50%; background: rgba(28, 39, 58, .7); }
.history-card.is-missing .cover-box:hover .play-overlay { opacity: 0; }
.history-card.is-missing .cover-box:hover .cover-img { transform: none; }
.history-card.is-missing .play-btn,
.history-card.is-missing .compact-play-btn { background: var(--neutral-05); color: var(--text-muted); box-shadow: none; opacity: 1; cursor: not-allowed; }
.missing-icon { color: #bd802e; }
.cleanup-error { color: var(--danger); font-size: 12px; margin-top: 8px; }

.cover-box {
  position: relative;
  width: 100px;
  height: 62px;
  border-radius: var(--radius-sm);
  overflow: hidden;
  flex-shrink: 0;
  cursor: pointer;
  background: rgba(0, 0, 0, 0.35);
  box-shadow: var(--cover-ring);
}

.cover-img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  transition: transform var(--transition-fast);
}

.play-overlay {
  position: absolute;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
  display: flex;
  align-items: center;
  justify-content: center;
  opacity: 0;
  transition: opacity var(--transition-fast);
}

.play-icon {
  color: #fff;
}

.cover-box:hover .play-overlay {
  opacity: 1;
}

.cover-box:hover .cover-img {
  transform: scale(1.05);
}

.cover-placeholder {
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

.content-box {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 6px;
  overflow: hidden;
  min-width: 0;
}

.title-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.item-title {
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

.meta-row {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 11.5px;
  font-variant-numeric: tabular-nums;
}

.meta-item {
  display: flex;
  align-items: center;
  gap: 4px;
  color: var(--text-muted);
}

.success-icon {
  color: var(--success);
}

.btn-group {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.play-btn {
  height: 30px;
  padding: 0 12px;
  font-size: 12px;
  gap: 4px;
}

.compact-play-btn {
  color: var(--bili-pink);
}

.dir-btn, .del-btn {
  width: 30px;
  height: 30px;
}

.del-btn:hover {
  color: var(--danger);
}

/* Empty State */
.empty-history {
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

/* Modal Styles */
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
  max-width: 460px;
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

.modal-icon-warning {
  color: #f59e0b;
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
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.modal-desc {
  font-size: 13.5px;
  color: var(--text-secondary);
  line-height: 1.5;
  margin: 0;
}

.task-preview-box,
.batch-preview-list {
  background: var(--neutral-03);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 5px;
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

.preview-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  word-break: break-all;
}

.preview-part {
  font-size: 12px;
  color: var(--bili-blue);
}

.preview-path {
  font-size: 11px;
  color: var(--text-muted);
  word-break: break-all;
  font-family: monospace;
}

.checkbox-label {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  cursor: pointer;
  padding: 10px 12px;
  background: var(--neutral-03);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  transition: all var(--transition-fast);
}

.checkbox-label:hover {
  background: var(--neutral-05);
  border-color: var(--neutral-15);
}

.custom-checkbox {
  margin-top: 3px;
  accent-color: var(--danger);
  width: 15px;
  height: 15px;
  cursor: pointer;
}

.checkbox-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.checkbox-main {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-primary);
}

.danger-text {
  color: var(--danger);
}

.checkbox-sub {
  font-size: 11.5px;
  color: var(--text-muted);
  line-height: 1.4;
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
  filter: brightness(1.15);
  box-shadow: 0 2px 8px rgba(239, 68, 68, 0.4);
}

.btn-danger:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

@keyframes fadeIn {
  from { opacity: 0; }
  to { opacity: 1; }
}
</style>
