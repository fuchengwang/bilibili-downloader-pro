<script setup lang="ts">
import { ref, computed } from 'vue'
import {
  Play,
  Pause,
  RotateCcw,
  Trash2,
  FolderOpen,
  AlertCircle,
  Clock,
  Zap,
  HardDrive,
  Film
} from 'lucide-vue-next'
import { downloader } from '../../wailsjs/go/models'

const props = defineProps<{
  tasks: downloader.DownloadTask[]
}>()

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
  (e: 'clear-completed'): void
}>()

const currentFilter = ref<'all' | 'downloading' | 'paused' | 'error'>('all')

function normalizeImg(url?: string) {
  if (!url) return ''
  if (url.startsWith('//')) return 'https:' + url
  if (url.startsWith('http://')) return 'https://' + url.substring(7)
  return url
}

const filteredTasks = computed(() => {
  const activeTasks = props.tasks.filter(t => t.status !== 'completed')
  if (currentFilter.value === 'all') return activeTasks
  if (currentFilter.value === 'downloading') return activeTasks.filter(t => t.status === 'downloading' || t.status === 'merging')
  if (currentFilter.value === 'paused') return activeTasks.filter(t => t.status === 'paused' || t.status === 'queued')
  if (currentFilter.value === 'error') return activeTasks.filter(t => t.status === 'error' || t.status === 'cancelled')
  return activeTasks
})
</script>

<template>
  <div class="queue-container">
    <!-- Queue Header & Action Bar -->
    <div class="queue-header">
      <div class="filter-tabs">
        <button
          class="tab-btn"
          :class="{ active: currentFilter === 'all' }"
          @click="currentFilter = 'all'"
        >
          全部 ({{ tasks.filter(t => t.status !== 'completed').length }})
        </button>
        <button
          class="tab-btn"
          :class="{ active: currentFilter === 'downloading' }"
          @click="currentFilter = 'downloading'"
        >
          下载中 ({{ tasks.filter(t => t.status === 'downloading' || t.status === 'merging').length }})
        </button>
        <button
          class="tab-btn"
          :class="{ active: currentFilter === 'paused' }"
          @click="currentFilter = 'paused'"
        >
          等待/暂停 ({{ tasks.filter(t => t.status === 'paused' || t.status === 'queued').length }})
        </button>
      </div>

      <div class="queue-actions">
        <button class="btn-secondary q-action-btn" @click="emit('pause-all')">
          <Pause :size="13" />
          <span>全部暂停</span>
        </button>
        <button class="btn-secondary q-action-btn" @click="emit('resume-all')">
          <Play :size="13" />
          <span>全部继续</span>
        </button>
      </div>
    </div>

    <!-- Task List -->
    <div v-if="filteredTasks.length > 0" class="task-list">
      <div
        v-for="task in filteredTasks"
        :key="task.id"
        class="task-card"
        :class="'status-' + task.status"
      >
        <div class="task-cover-box">
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
          <div class="task-title-row">
            <span class="task-title" :title="task.title">{{ task.title }}</span>
            <span v-if="task.partTitle && task.partTitle !== task.title" class="part-badge" :title="task.partTitle">
              {{ task.partTitle }}
            </span>
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
                <span>{{ task.errorMsg || '下载失败' }}</span>
              </span>
              <span v-else-if="task.status === 'cancelled'" class="status-tag tag-cancelled">
                <span>已取消</span>
              </span>

              <!-- Transferred Size -->
              <span v-if="task.sizeStr" class="metric-text size-metric">
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
        </div>

        <!-- Task Actions -->
        <div class="task-btn-group">
          <button
            v-if="task.status === 'downloading'"
            class="btn-icon task-btn"
            @click="emit('pause-task', task.id)"
            title="暂停"
          >
            <Pause :size="15" />
          </button>

          <button
            v-else-if="task.status === 'paused' || task.status === 'queued'"
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
  </div>
</template>

<style scoped>
.queue-container {
  display: flex;
  flex-direction: column;
  gap: 14px;
  width: 100%;
  max-width: 920px;
  margin: 0 auto;
}

.queue-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border-subtle);
}

.filter-tabs {
  display: flex;
  align-items: center;
  gap: 4px;
}

.tab-btn {
  padding: 5px 10px;
  font-size: 12px;
  background: transparent;
  color: var(--text-muted);
  border-radius: var(--radius-sm);
}

.tab-btn:hover {
  background: var(--neutral-05);
  color: var(--text-primary);
}

.tab-btn.active {
  background: rgba(251, 114, 153, 0.12);
  color: var(--bili-pink);
  font-weight: 600;
}

.queue-actions {
  display: flex;
  align-items: center;
  gap: 6px;
}

.q-action-btn {
  height: 28px;
  font-size: 11.5px;
  padding: 0 8px;
  gap: 4px;
}

.task-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.task-card {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 10px 12px;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-sm);
  transition: all var(--transition-fast);
}

.task-card:hover {
  background: var(--bg-card-hover);
  border-color: var(--neutral-12);
}

.task-cover-box {
  position: relative;
  width: 96px;
  height: 60px;
  border-radius: var(--radius-sm);
  overflow: hidden;
  flex-shrink: 0;
  background: rgba(0, 0, 0, 0.3);
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
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.part-badge {
  font-size: 10.5px;
  color: var(--bili-blue);
  background: rgba(0, 174, 236, 0.12);
  padding: 1px 5px;
  border-radius: var(--radius-xs);
  white-space: nowrap;
  flex-shrink: 0;
}

.progress-wrap {
  width: 100%;
}

.progress-bar {
  width: 100%;
  height: 5px;
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
  overflow: hidden;
}

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
  gap: 3px;
  color: var(--text-muted);
}

.progress-percent {
  font-weight: 600;
  color: var(--text-secondary);
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
</style>
