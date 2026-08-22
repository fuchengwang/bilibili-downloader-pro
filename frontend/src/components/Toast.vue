<script setup lang="ts">
import {
  CheckCircle2,
  AlertCircle,
  Info,
  Sparkles,
  X
} from 'lucide-vue-next'

export interface ToastItem {
  id: string
  message: string
  type: 'success' | 'error' | 'info' | 'clipboard'
  actionUrl?: string
}

defineProps<{
  toasts: ToastItem[]
}>()

const emit = defineEmits<{
  (e: 'close-toast', id: string): void
  (e: 'action-clipboard', url: string, id: string): void
}>()
</script>

<template>
  <div class="toast-container">
    <div
      v-for="t in toasts"
      :key="t.id"
      class="toast-card"
      :class="'toast-' + t.type"
    >
      <div class="toast-icon">
        <CheckCircle2 v-if="t.type === 'success'" :size="18" class="icon-success" />
        <AlertCircle v-else-if="t.type === 'error'" :size="18" class="icon-error" />
        <Sparkles v-else-if="t.type === 'clipboard'" :size="18" class="icon-clipboard" />
        <Info v-else :size="18" class="icon-info" />
      </div>

      <div class="toast-text">
        {{ t.message }}
      </div>

      <button
        v-if="t.type === 'clipboard' && t.actionUrl"
        class="btn-primary toast-action-btn"
        @click="emit('action-clipboard', t.actionUrl, t.id)"
      >
        <span>一键解析</span>
      </button>

      <button class="btn-icon toast-close" @click="emit('close-toast', t.id)">
        <X :size="14" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.toast-container {
  position: fixed;
  top: 20px;
  right: 20px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  z-index: 9999;
  pointer-events: none;
}

.toast-card {
  pointer-events: auto;
  min-width: 280px;
  max-width: 420px;
  padding: 12px 14px;
  border-radius: var(--radius-md);
  background: rgba(22, 27, 39, 0.95);
  backdrop-filter: blur(16px);
  border: 1px solid var(--border-subtle);
  box-shadow: var(--shadow-lg);
  display: flex;
  align-items: center;
  gap: 10px;
  animation: slideInRight 0.25s cubic-bezier(0.16, 1, 0.3, 1);
}

.toast-success {
  border-color: rgba(16, 185, 129, 0.4);
}

.icon-success {
  color: var(--success);
}

.toast-error {
  border-color: rgba(239, 68, 68, 0.4);
}

.icon-error {
  color: var(--danger);
}

.toast-clipboard {
  border-color: rgba(251, 114, 153, 0.4);
  background: rgba(30, 24, 38, 0.96);
}

.icon-clipboard {
  color: var(--bili-pink);
}

.icon-info {
  color: var(--bili-blue);
}

.toast-icon {
  flex-shrink: 0;
  display: flex;
  align-items: center;
}

.toast-text {
  flex: 1;
  font-size: 13px;
  color: var(--text-primary);
  line-height: 1.4;
}

.toast-action-btn {
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
  flex-shrink: 0;
}

.toast-close {
  width: 24px;
  height: 24px;
  flex-shrink: 0;
}

@keyframes slideInRight {
  from {
    opacity: 0;
    transform: translateX(30px);
  }
  to {
    opacity: 1;
    transform: translateX(0);
  }
}
</style>
