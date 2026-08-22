<script setup lang="ts">
import { FolderOpen, Zap, ClipboardCheck } from 'lucide-vue-next'

defineProps<{
  title: string
  totalSpeedStr: string
  autoClipboard: boolean
}>()

const emit = defineEmits<{
  (e: 'open-dir'): void
}>()
</script>

<template>
  <header class="app-header" style="--wails-draggable: drag">
    <div class="header-left">
      <h1 class="page-title">{{ title }}</h1>
    </div>

    <div class="header-right" style="--wails-draggable: no-drag">
      <!-- Global Speed Indicator -->
      <div v-if="totalSpeedStr && totalSpeedStr !== '0 KB/s'" class="speed-badge">
        <Zap class="speed-icon" :size="13" />
        <span>{{ totalSpeedStr }}</span>
      </div>

      <!-- Auto Clipboard Detection Badge -->
      <div v-if="autoClipboard" class="clipboard-tag" title="剪贴板自动感应已开启">
        <ClipboardCheck :size="13" />
        <span>剪贴板感应</span>
      </div>

      <!-- Open Output Folder Button -->
      <button class="btn-secondary open-dir-btn" @click="emit('open-dir')" title="打开下载目录">
        <FolderOpen :size="14" />
        <span>下载目录</span>
      </button>
    </div>
  </header>
</template>

<style scoped>
.app-header {
  height: 54px;
  min-height: 54px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px 0 20px;
  border-bottom: 1px solid var(--border-subtle);
  background: rgba(12, 14, 20, 0.6);
  backdrop-filter: blur(16px);
  flex-shrink: 0;
}

.header-left {
  display: flex;
  align-items: center;
}

.page-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: -0.2px;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 10px;
}

.speed-badge {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 4px 10px;
  border-radius: var(--radius-full);
  background: rgba(0, 174, 236, 0.12);
  color: var(--bili-blue);
  border: 1px solid rgba(0, 174, 236, 0.25);
  font-size: 11.5px;
  font-weight: 600;
}

.clipboard-tag {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border-radius: var(--radius-xs);
  background: rgba(255, 255, 255, 0.05);
  color: var(--text-muted);
  font-size: 11px;
}

.open-dir-btn {
  height: 30px;
  padding: 0 10px;
  font-size: 12px;
  gap: 5px;
}
</style>
