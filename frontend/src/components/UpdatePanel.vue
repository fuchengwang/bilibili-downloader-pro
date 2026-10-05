<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { RefreshCw, Download, Pause, CheckCircle2, X } from 'lucide-vue-next'
import type { updater } from '../../wailsjs/go/models'
import {
  CheckForUpdates, SetAutomaticUpdateCheck, DownloadUpdate,
  PauseUpdateDownload, RetryUpdateDownload, RestartForUpdate
} from '../../wailsjs/go/main/App'

const props = defineProps<{ state: updater.State }>()
const actionError = ref('')
const requesting = ref(false)
const savingChoice = ref(false)
const dismissed = ref(false)
const busy = computed(() => ['checking', 'downloading', 'preparing', 'restarting'].includes(props.state.phase))
const ready = computed(() => props.state.phase === 'ready')
const error = computed(() => actionError.value || props.state.error)
const progress = computed(() => props.state.total > 0
  ? Math.max(0, Math.min(100, Math.floor(props.state.downloaded / props.state.total * 100))) : 0)
const status = computed(() => {
  switch (props.state.phase) {
    case 'checking': return '正在检查更新…'
    case 'downloading': return '正在下载更新'
    case 'preparing': return '正在准备更新…'
    case 'paused': return '下载已暂停'
    case 'ready': return '更新已准备好'
    case 'restarting': return '正在重启…'
    default: return props.state.hasUpdate ? '有可用更新' : props.state.checked ? '未发现新版本' : ''
  }
})
watch(() => [props.state.version, props.state.phase], ([version, phase], previous) => {
  actionError.value = ''
  if (version !== previous?.[0] || (phase === 'ready' && previous?.[1] !== 'ready')) dismissed.value = false
})

function formatBytes(bytes: number) {
  if (bytes < 1024) return `${Math.max(0, bytes)} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}
async function act(fn: () => Promise<void>) {
  if (requesting.value) return
  requesting.value = true
  actionError.value = ''
  try { await fn() } catch (err: any) { actionError.value = err?.message || String(err) }
  finally { requesting.value = false }
}
async function changeAutomatic(event: Event) {
  const input = event.target as HTMLInputElement
  const selected = input.checked
  savingChoice.value = true
  actionError.value = ''
  try { await SetAutomaticUpdateCheck(selected) }
  catch (err: any) { input.checked = props.state.autoCheck; actionError.value = err?.message || String(err) }
  finally { savingChoice.value = false }
}
</script>

<template>
  <section class="update-card" aria-labelledby="update-title">
    <div class="update-header">
      <span class="update-icon"><RefreshCw :size="18" /></span>
      <h3 id="update-title">软件更新</h3>
      <span class="current-version">当前版本 v{{ state.currentVersion }}</span>
    </div>
    <div class="update-body">
      <div class="update-controls">
        <label class="automatic-choice">
          <input type="checkbox" :checked="state.autoCheck" :disabled="savingChoice" @change="changeAutomatic" />
          <span>自动检查更新</span>
        </label>
        <button class="update-button secondary" :disabled="busy || ready || requesting" @click="act(CheckForUpdates)">
          <RefreshCw :size="14" :class="{ spinning: state.phase === 'checking' }" />
          {{ state.phase === 'checking' ? '正在检查…' : '检查更新' }}
        </button>
      </div>

      <div v-if="state.hasUpdate || status" class="release-row">
        <div class="release-info">
          <strong v-if="state.version">v{{ state.version }}</strong>
          <span>{{ status }}</span>
        </div>
        <button v-if="state.phase === 'available' || state.phase === 'paused'" class="update-button primary" :disabled="requesting" @click="act(DownloadUpdate)">
          <Download :size="14" />{{ state.phase === 'paused' ? '继续更新' : '下载更新' }}
        </button>
        <button v-else-if="state.phase === 'downloading'" class="update-button secondary" :disabled="requesting" @click="act(PauseUpdateDownload)"><Pause :size="14" />暂停</button>
        <button v-else-if="ready && dismissed" class="update-button primary" :disabled="requesting" @click="act(RestartForUpdate)">重启使用新版</button>
      </div>
      <p v-if="state.notes && state.hasUpdate" class="release-notes">{{ state.notes }}</p>

      <div v-if="['downloading', 'paused', 'preparing'].includes(state.phase)" class="download-progress">
        <div class="progress-track" role="progressbar" aria-label="更新下载进度" :aria-valuenow="progress" aria-valuemin="0" aria-valuemax="100">
          <div class="progress-fill" :style="{ width: progress + '%' }"></div>
        </div>
        <div class="progress-label"><span>{{ formatBytes(state.downloaded) }} / {{ formatBytes(state.total) }}</span><span>{{ progress }}%</span></div>
      </div>

      <div v-if="ready && !dismissed" class="ready-message" role="status">
        <div class="ready-heading"><CheckCircle2 :size="17" /><strong>更新完成</strong><button class="dismiss-button" aria-label="关闭更新提示" @click="dismissed = true"><X :size="15" /></button></div>
        <p>重启后即可使用新版。</p>
        <div class="ready-actions">
          <button class="update-button primary" :disabled="requesting" @click="act(RestartForUpdate)">立即重启</button>
          <button class="update-button secondary" :disabled="requesting" @click="dismissed = true">暂不重启</button>
        </div>
      </div>

      <div v-if="error" class="update-error" role="status">
        <p>{{ error }}</p>
        <button v-if="state.hasUpdate && !busy" class="text-button" :disabled="requesting" @click="act(RetryUpdateDownload)">重新下载</button>
      </div>
    </div>
  </section>
</template>

<style scoped>
.update-card { background: var(--bg-card); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); box-shadow: var(--shadow-sm); overflow: hidden; }
.update-header { display: flex; align-items: center; gap: 12px; padding: 14px 18px; border-bottom: 1px solid var(--border-subtle); background: var(--neutral-015); }
.update-header h3 { margin: 0; font-size: 14px; font-weight: 600; color: var(--text-primary); }
.update-icon { width: 32px; height: 32px; border-radius: var(--radius-sm); background: rgba(0,174,236,.12); color: #00aeec; display: flex; align-items: center; justify-content: center; }
.current-version { margin-left: auto; font-size: 12px; color: var(--text-muted); }
.update-body { padding: 18px; display: flex; flex-direction: column; gap: 14px; }
.update-controls, .release-row, .ready-heading, .ready-actions { display: flex; align-items: center; gap: 10px; }
.update-controls, .release-row { justify-content: space-between; }
.automatic-choice { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text-primary); cursor: pointer; }
.automatic-choice input { accent-color: #00aeec; width: 15px; height: 15px; cursor: pointer; }
.update-button { display: inline-flex; align-items: center; justify-content: center; gap: 6px; border-radius: var(--radius-sm); padding: 7px 12px; font-size: 12px; border: 1px solid transparent; cursor: pointer; white-space: nowrap; }
.update-button.primary { background: #008fc9; color: #fff; }
.update-button.primary:hover { background: #007fb4; }
.update-button.secondary { background: var(--neutral-05); color: var(--text-secondary); border-color: var(--border-subtle); }
.update-button:disabled, .text-button:disabled { opacity: .55; cursor: default; }
.release-info { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; font-size: 12px; color: var(--text-secondary); }
.release-info strong { color: var(--text-primary); font-size: 14px; }
.release-notes { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 12px; line-height: 1.7; max-height: 180px; overflow-y: auto; color: var(--text-secondary); }
.progress-track { height: 5px; border-radius: 5px; overflow: hidden; background: var(--neutral-08); }
.progress-fill { height: 100%; background: #00aeec; transition: width .15s ease; }
.progress-label { display: flex; justify-content: space-between; margin-top: 8px; font-size: 11px; color: var(--text-muted); font-variant-numeric: tabular-nums; }
.ready-message { background: rgba(16,185,129,.07); border: 1px solid rgba(16,185,129,.25); border-radius: var(--radius-sm); padding: 12px; }
.ready-heading { color: #10b981; font-size: 13px; }
.ready-message p { margin: 7px 0 12px; font-size: 12px; color: var(--text-secondary); }
.dismiss-button { margin-left: auto; background: transparent; border: none; color: var(--text-muted); padding: 2px; display: inline-flex; cursor: pointer; }
.update-error { display: flex; align-items: flex-start; gap: 12px; font-size: 12px; color: var(--text-secondary); line-height: 1.6; }
.update-error p { margin: 0; flex: 1; overflow-wrap: anywhere; }
.text-button { background: transparent; border: none; color: #008fc9; padding: 0; cursor: pointer; font-size: 12px; white-space: nowrap; }
.spinning { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .spinning { animation: none; } .progress-fill { transition: none; } }
</style>
