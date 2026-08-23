<script setup lang="ts">
import { ref, computed } from 'vue'
import {
  Play,
  FolderOpen,
  Trash2,
  CheckCircle2,
  Search,
  HardDrive,
  Clock,
  Film
} from 'lucide-vue-next'
import { downloader } from '../../wailsjs/go/models'

const props = defineProps<{
  tasks: downloader.DownloadTask[]
}>()

const emit = defineEmits<{
  (e: 'open-file', path: string, task?: downloader.DownloadTask): void
  (e: 'open-dir', path: string): void
  (e: 'delete-task', id: string, deleteFile: boolean): void
  (e: 'request-delete', task: downloader.DownloadTask): void
  (e: 'clear-completed'): void
}>()

const searchKey = ref('')

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
  return list.filter(t => t.title.toLowerCase().includes(kw) || t.partTitle.toLowerCase().includes(kw))
})

function formatCompletedDate(timestamp: number): string {
  if (!timestamp) return ''
  const date = new Date(timestamp * 1000)
  return `${date.getMonth() + 1}月${date.getDate()}日 ${date.getHours().toString().padStart(2, '0')}:${date.getMinutes().toString().padStart(2, '0')}`
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
        <span class="count-text">共 {{ completedTasks.length }} 个文件</span>
        <button
          v-if="completedTasks.length > 0"
          class="btn-secondary clear-btn"
          @click="emit('clear-completed')"
        >
          <Trash2 :size="13" />
          <span>清空记录</span>
        </button>
      </div>
    </div>

    <!-- History Task List -->
    <div v-if="completedTasks.length > 0" class="history-list">
      <div
        v-for="task in completedTasks"
        :key="task.id"
        class="history-card"
      >
        <div class="cover-box" @click="emit('open-file', task.outputPath, task)">
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
          <span v-if="task.qualityLabel" class="quality-badge">{{ task.qualityLabel }}</span>
        </div>

        <div class="content-box">
          <div class="title-row">
            <span class="item-title" :title="task.title">{{ task.title }}</span>
            <span v-if="task.partTitle && task.partTitle !== task.title" class="part-badge" :title="task.partTitle">
              {{ task.partTitle }}
            </span>
          </div>

          <div class="meta-row">
            <span class="meta-item">
              <CheckCircle2 :size="12" class="success-icon" />
              <span>下载完成</span>
            </span>

            <span v-if="task.sizeStr" class="meta-item">
              <HardDrive :size="11" />
              <span>{{ task.sizeStr.split('/')[0]?.trim() || task.sizeStr }}</span>
            </span>

            <span v-if="task.completedAt" class="meta-item">
              <Clock :size="11" />
              <span>{{ formatCompletedDate(task.completedAt) }}</span>
            </span>
          </div>
        </div>

        <div class="btn-group">
          <button class="btn-primary play-btn" @click="emit('open-file', task.outputPath, task)" title="播放视频">
            <Play :size="13" />
            <span>播放</span>
          </button>

          <button class="btn-icon dir-btn" @click="emit('open-dir', task.outputPath)" title="在访达/资源管理器中显示">
            <FolderOpen :size="14" />
          </button>

          <button class="btn-icon del-btn" @click="emit('delete-task', task.id, false)" title="清除记录">
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
  </div>
</template>

<style scoped>
.history-container {
  display: flex;
  flex-direction: column;
  gap: 14px;
  width: 100%;
  max-width: 920px;
  margin: 0 auto;
}

.history-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--border-subtle);
}

.search-box {
  display: flex;
  align-items: center;
  gap: 6px;
  background: var(--bg-input);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  padding: 0 10px;
  width: 220px;
}

.search-icon {
  color: var(--text-muted);
}

.history-search {
  height: 30px;
  background: transparent;
  border: none;
  font-size: 12px;
  color: #fff;
  flex: 1;
}

.history-search:focus {
  border: none;
  box-shadow: none;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.count-text {
  font-size: 12px;
  color: var(--text-muted);
}

.clear-btn {
  height: 28px;
  font-size: 11.5px;
  padding: 0 8px;
  gap: 4px;
}

.history-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.history-card {
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

.history-card:hover {
  background: var(--bg-card-hover);
  border-color: rgba(255, 255, 255, 0.12);
}

.cover-box {
  position: relative;
  width: 100px;
  height: 62px;
  border-radius: var(--radius-sm);
  overflow: hidden;
  flex-shrink: 0;
  cursor: pointer;
  background: rgba(0, 0, 0, 0.3);
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
  background: rgba(255, 255, 255, 0.05);
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
  color: var(--bili-blue);
  background: rgba(0, 174, 236, 0.12);
  padding: 1px 5px;
  border-radius: var(--radius-xs);
  white-space: nowrap;
  flex-shrink: 0;
}

.meta-row {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 11.5px;
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
  background: rgba(255, 255, 255, 0.04);
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
