<script setup lang="ts">
import { ref, onBeforeUnmount } from 'vue'
import {
  Folder,
  FolderOpen,
  Cpu,
  ClipboardCheck,
  ShieldCheck,
  KeyRound,
  Unlink,
  Eye,
  EyeOff,
  Sun
} from 'lucide-vue-next'
import UpdatePanel from './UpdatePanel.vue'
import AppSelect from './AppSelect.vue'
import { config, license, updater } from '../../wailsjs/go/models'
import {
  GetSettings,
  SaveSettings,
  SelectDirectory,
  OpenDirectory,
  DeactivateLicense
} from '../../wailsjs/go/main/App'

const props = defineProps<{
  initialSettings: config.Settings
  updateState: updater.State
  licenseStatus: license.LicenseStatus | null
}>()

const emit = defineEmits<{
  (e: 'settings-saved', s: config.Settings): void
  (e: 'show-toast', msg: string, type: 'success' | 'error' | 'info'): void
  (e: 'license-deactivated', key: string): void
  (e: 'theme-changed', value: string): void
}>()

const form = ref<config.Settings>({
  downloadDir: props.initialSettings?.downloadDir || '',
  defaultQuality: props.initialSettings?.defaultQuality || 'highest',
  defaultCodec: props.initialSettings?.defaultCodec || 'auto',
  maxConcurrent: props.initialSettings?.maxConcurrent || 3,
  threadsPerTask: props.initialSettings?.threadsPerTask || 4,
  downloadLimitKB: props.initialSettings?.downloadLimitKB ?? 0,
  autoMerge: props.initialSettings?.autoMerge ?? true,
  deleteTempFiles: props.initialSettings?.deleteTempFiles ?? true,
  autoClipboard: props.initialSettings?.autoClipboard ?? true,
  fileNameTemplate: props.initialSettings?.fileNameTemplate || '{title} - {part}',
  theme: props.initialSettings?.theme || 'dark'
})

const isSaving = ref(false)
const saveStatus = ref('修改后自动保存')
const limitEnabled = ref(form.value.downloadLimitKB > 0)
const limitUnit = ref<'KB' | 'MB'>(form.value.downloadLimitKB >= 1024 || !limitEnabled.value ? 'MB' : 'KB')
const downloadLimitInput = ref(limitEnabled.value ? String(form.value.downloadLimitKB / (limitUnit.value === 'MB' ? 1024 : 1)) : '2')

function restoreLimitControls(limit: number) {
  limitEnabled.value = limit > 0
  if (limit > 0) {
    limitUnit.value = limit >= 1024 ? 'MB' : 'KB'
    downloadLimitInput.value = String(limit / (limitUnit.value === 'MB' ? 1024 : 1))
  }
}

async function handleLimitToggle() {
  if (limitTimer) clearTimeout(limitTimer)
  limitTimer = null
  limitError.value = ''
  if (!limitEnabled.value) {
    form.value.downloadLimitKB = 0
    await savePreference('downloadLimitKB', 0)
  } else await handleSave()
}

async function handleLimitUnitChange() {
  await handleSave()
}
const limitError = ref('')
let lastSavedSettings = { ...form.value }
const editVersions: Partial<Record<keyof config.Settings, number>> = {}
let saveQueue: Promise<void> = Promise.resolve()
let pendingSaves = 0
let limitTimer: ReturnType<typeof setTimeout> | null = null
let directoryTimer: ReturnType<typeof setTimeout> | null = null
let saveNoticeTimer: ReturnType<typeof setTimeout> | null = null
let savedSinceNotice = false

function cancelSaveNotice() {
  if (saveNoticeTimer) clearTimeout(saveNoticeTimer)
  saveNoticeTimer = null
}

function scheduleSaveNotice() {
  cancelSaveNotice()
  if (!savedSinceNotice) return
  saveNoticeTimer = setTimeout(() => {
    saveNoticeTimer = null
    if (pendingSaves || limitTimer || directoryTimer) return
    savedSinceNotice = false
    emit('show-toast', '偏好设置已自动保存', 'success')
  }, 600)
}

// Each change merges only its own field into the latest persisted settings.
// Serialize writes so fast edits and appearance changes cannot overwrite each other.
function savePreference<K extends keyof config.Settings>(key: K, value: config.Settings[K]) {
  cancelSaveNotice()
  const limitDraft = `${limitEnabled.value}|${downloadLimitInput.value}|${limitUnit.value}`
  const revision = (editVersions[key] || 0) + 1
  editVersions[key] = revision
  pendingSaves++
  isSaving.value = true
  saveStatus.value = '正在自动保存…'
  const operation = saveQueue.then(async () => {
    let saved: config.Settings | undefined
    try {
      saved = await GetSettings()
      const settings = { ...saved, [key]: value }
      if (saved[key] !== value) {
        await SaveSettings(settings)
        savedSinceNotice = true
      }
      lastSavedSettings = settings
      emit('settings-saved', settings)
      if (key === 'theme') emit('theme-changed', String(value))
      saveStatus.value = '已自动保存'
    } catch (err: any) {
      savedSinceNotice = false
      // Roll back only this failed edit; preserve any newer choice queued after it.
      if (editVersions[key] === revision && form.value[key] === value) {
        form.value[key] = (saved || lastSavedSettings)[key]
        if (key === 'downloadLimitKB' && limitDraft === `${limitEnabled.value}|${downloadLimitInput.value}|${limitUnit.value}`) {
          restoreLimitControls(form.value.downloadLimitKB)
        }
      }
      saveStatus.value = '保存失败，请重新修改后重试'
      emit('show-toast', '自动保存失败: ' + (err?.message || err), 'error')
    } finally {
      pendingSaves--
      isSaving.value = pendingSaves > 0
      if (pendingSaves > 0) saveStatus.value = '正在自动保存…'
      else scheduleSaveNotice()
    }
  })
  saveQueue = operation
  return operation
}

function scheduleLimitSave() {
  cancelSaveNotice()
  if (limitTimer) clearTimeout(limitTimer)
  limitTimer = setTimeout(() => { limitTimer = null; void handleSave() }, 500)
}

function saveDirectory() {
  if (directoryTimer) clearTimeout(directoryTimer)
  directoryTimer = null
  if (form.value.downloadDir.trim()) void savePreference('downloadDir', form.value.downloadDir.trim())
  else saveStatus.value = '下载目录不能为空，尚未保存'
}

function scheduleDirectorySave() {
  cancelSaveNotice()
  if (directoryTimer) clearTimeout(directoryTimer)
  directoryTimer = setTimeout(saveDirectory, 500)
}

onBeforeUnmount(() => {
  cancelSaveNotice()
  // Flush text edits still waiting on the typing delay when this view closes.
  if (limitTimer) { clearTimeout(limitTimer); limitTimer = null; void handleSave() }
  if (directoryTimer) saveDirectory()
})
const showDeactivateConfirm = ref(false)
const isDeactivating = ref(false)
const deactivateError = ref('')
const showLicenseKey = ref(false)

function maskLicenseKey(key?: string) {
  if (!key) return '—'
  const parts = key.split('-')
  if (parts.length > 2) return `${parts[0]}-••••-••••-${parts[parts.length - 1]}`
  return key.length > 8 ? `${key.slice(0, 4)}••••${key.slice(-4)}` : '••••••••'
}

async function handleDeactivate() {
  if (isDeactivating.value) return
  const key = props.licenseStatus?.license_key || ''
  isDeactivating.value = true
  deactivateError.value = ''
  try {
    const result: any = await DeactivateLicense()
    if (!result?.success) {
      const message = result?.message || '解绑失败，请稍后重试'
      deactivateError.value = /connect|network|timeout|连接|网络/i.test(message)
        ? '无法连接激活服务器，当前授权仍保留，请检查网络后重试'
        : message
      return
    }
    emit('license-deactivated', key)
  } catch (err: any) {
    const message = err?.message || String(err)
    deactivateError.value = /connect|network|timeout|连接|网络/i.test(message)
      ? '无法连接激活服务器，当前授权仍保留，请检查网络后重试'
      : message
  } finally {
    isDeactivating.value = false
  }
}

async function handleBrowseDir() {
  try {
    const path = await SelectDirectory()
    if (path) {
      form.value.downloadDir = path
      emit('show-toast', '已成功更改下载目录', 'success')
      await savePreference('downloadDir', path)
    }
  } catch (err: any) {
    emit('show-toast', '选择目录失败: ' + err.message, 'error')
  }
}

async function handleOpenDir() {
  try {
    await OpenDirectory(form.value.downloadDir)
  } catch (err: any) {
    emit('show-toast', '打开目录失败: ' + err.message, 'error')
  }
}

async function handleSave() {
  if (limitTimer) clearTimeout(limitTimer)
  limitTimer = null
  if (!limitEnabled.value) return
  const input = downloadLimitInput.value.trim()
  const limit = Math.floor(Number(input) * (limitUnit.value === 'MB' ? 1024 : 1) + 1e-7)
  if (!/^(?:\d+(?:\.\d*)?|\.\d+)$/.test(input) || !Number.isSafeInteger(limit) || limit < 1 || limit > 1048576) {
    cancelSaveNotice()
    savedSinceNotice = false
    saveStatus.value = '限速未保存：请输入有效的下载速度'
    limitError.value = '请输入有效速度，范围为 1 KB/s 到 1024 MB/s'
    return
  }
  limitError.value = ''
  form.value.downloadLimitKB = limit
  await savePreference('downloadLimitKB', limit)
}

const themeOptions = [
  { value: 'light', label: '浅色模式' },
  { value: 'dark', label: '深色模式' },
  { value: 'system', label: '跟随系统' }
]

const qualityOptions = [
  { value: 'highest', label: '自动最高画质' },
  { value: '127', label: '8K 超高清' },
  { value: '120', label: '4K 超清' },
  { value: '116', label: '1080P 60帧' },
  { value: '112', label: '1080P 高码率' },
  { value: '80', label: '1080P 高清' },
  { value: '64', label: '720P 高清' },
  { value: '32', label: '480P 清晰' }
]

const codecOptions = [
  { value: 'auto', label: '自动（优先兼容）' },
  { value: 'AVC', label: 'AVC / H.264' },
  { value: 'HEVC', label: 'HEVC / H.265' },
  { value: 'AV1', label: 'AV1' }
]

const threadOptions = [
  { value: 1, label: '1 线程' },
  { value: 2, label: '2 线程' },
  { value: 3, label: '3 线程' },
  { value: 4, label: '4 线程（推荐）' },
  { value: 6, label: '6 线程' },
  { value: 8, label: '8 线程' }
]

const limitUnitOptions = [
  { value: 'MB', label: 'MB/s' },
  { value: 'KB', label: 'KB/s' }
]
</script>

<template>
  <div class="settings-page">
    <div class="settings-container">
      <div class="settings-card">
        <div class="card-header">
          <div class="card-icon-box"><Sun :size="16" /></div>
          <div class="header-text">
            <h3 class="card-title">外观</h3>
          </div>
        </div>
        <div class="card-body">
          <div class="form-item">
            <label class="item-label" for="theme-select">界面模式</label>
            <AppSelect
              id="theme-select"
              :model-value="form.theme"
              :options="themeOptions"
              aria-label="界面模式"
              @update:model-value="val => { form.theme = String(val); savePreference('theme', form.theme) }"
            />
          </div>
        </div>
      </div>
      <!-- Section 1: 下载目录 -->
      <div class="settings-card">
        <div class="card-header">
          <div class="card-icon-box">
            <Folder :size="16" />
          </div>
          <div class="header-text">
            <h3 class="card-title">存储位置</h3>
          </div>
        </div>

        <div class="card-body">
          <div class="form-item">
            <label class="item-label">下载目录</label>
            <div class="dir-group">
              <input
                v-model="form.downloadDir"
                type="text"
                class="dir-input"
                placeholder="正在获取默认下载目录..."
                @input="scheduleDirectorySave" @blur="saveDirectory" @keydown.enter="saveDirectory"
              />
              <button class="btn-secondary browse-btn" @click="handleBrowseDir">
                <span>更改目录</span>
              </button>
              <button class="btn-secondary open-btn" @click="handleOpenDir" title="在访达/资源管理器中打开">
                <FolderOpen :size="15" />
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- Section 2: 画质与并发偏好 -->
      <div class="settings-card">
        <div class="card-header">
          <div class="card-icon-box">
            <Cpu :size="16" />
          </div>
          <div class="header-text">
            <h3 class="card-title">下载与画质</h3>
          </div>
        </div>

        <div class="card-body">
          <div class="grid-2">
            <div class="form-item">
              <label class="item-label">首选画质</label>
              <AppSelect
                :model-value="form.defaultQuality"
                :options="qualityOptions"
                aria-label="首选画质"
                @update:model-value="val => { form.defaultQuality = String(val); savePreference('defaultQuality', form.defaultQuality) }"
              />
            </div>

            <div class="form-item">
              <label class="item-label">视频编码</label>
              <AppSelect
                :model-value="form.defaultCodec"
                :options="codecOptions"
                aria-label="视频编码"
                @update:model-value="val => { form.defaultCodec = String(val); savePreference('defaultCodec', form.defaultCodec) }"
              />
            </div>
          </div>

          <div class="grid-2">
            <div class="form-item">
              <label class="item-label">分片线程</label>
              <AppSelect
                :model-value="form.threadsPerTask"
                :options="threadOptions"
                aria-label="分片线程"
                @update:model-value="val => { form.threadsPerTask = Number(val); savePreference('threadsPerTask', form.threadsPerTask) }"
              />
            </div>

            <div class="form-item">
              <label class="item-label">同时下载数</label>
              <div class="slider-box">
                <input
                  v-model.number="form.maxConcurrent"
                  type="range"
                  min="1"
                  max="10"
                  class="concurrency-range"
                  @input="savePreference('maxConcurrent', form.maxConcurrent)"
                />
                <span class="range-val-badge">{{ form.maxConcurrent }} 个任务</span>
              </div>
            </div>
          </div>
          <div class="form-item speed-limit-item">
            <div class="speed-limit-header">
              <div>
                <label class="item-label" for="limit-enabled">限制下载速度</label>
                <p class="card-subtitle">{{ limitEnabled ? '所有下载任务共享上限' : '不限速' }}</p>
              </div>
              <label class="speed-switch">
                <input id="limit-enabled" v-model="limitEnabled" type="checkbox" role="switch" aria-label="限制下载速度" @change="handleLimitToggle" />
                <span class="speed-switch-track" aria-hidden="true"></span>
              </label>
            </div>
            <div v-if="limitEnabled" class="speed-limit-controls">
              <div class="speed-limit-input">
                <input id="download-limit" v-model="downloadLimitInput" type="text" inputmode="decimal" autocomplete="off" class="dir-input" aria-label="下载速度上限" @input="scheduleLimitSave" @blur="handleSave" @keydown.enter="handleSave" />
                <AppSelect
                  :model-value="limitUnit"
                  :options="limitUnitOptions"
                  class="speed-limit-unit"
                  aria-label="下载速度单位"
                  @update:model-value="val => { limitUnit = val === 'KB' ? 'KB' : 'MB'; handleLimitUnitChange() }"
                />
              </div>
              <p v-if="limitError" class="limit-error" role="alert">{{ limitError }}</p>
            </div>
          </div>
        </div>
      </div>

      <!-- Section 3: 剪贴板感应与交互 -->
      <div class="settings-card">
        <div class="card-header">
          <div class="card-icon-box">
            <ClipboardCheck :size="16" />
          </div>
          <div class="header-text">
            <h3 class="card-title">交互与感应</h3>
          </div>
        </div>

        <div class="card-body">
          <div class="switch-row">
            <span class="switch-row-label">自动识别剪贴板 B 站链接</span>
            <label class="speed-switch">
              <input v-model="form.autoClipboard" type="checkbox" role="switch" aria-label="自动识别剪贴板 B 站链接" @change="savePreference('autoClipboard', form.autoClipboard)" />
              <span class="speed-switch-track" aria-hidden="true"></span>
            </label>
          </div>
        </div>
      </div>

      <p class="auto-save-status" role="status">{{ saveStatus }}</p>

      <UpdatePanel :state="updateState" />

      <!-- Section 4: 专业版授权 -->
      <div class="settings-card license-settings-card">
        <div class="card-header">
          <div class="card-icon-box license-icon-box">
            <ShieldCheck :size="18" />
          </div>
          <div class="header-text">
            <h3 class="card-title">专业版授权</h3>
          </div>
          <span class="license-status-badge">已激活</span>
        </div>

        <div class="card-body license-body">
          <div class="license-summary">
            <div class="license-detail">
              <KeyRound :size="15" />
              <div class="license-key-value"><span>激活码</span><strong :class="{ 'selectable-license-key': showLicenseKey }">{{ showLicenseKey ? (licenseStatus?.license_key || '—') : maskLicenseKey(licenseStatus?.license_key) }}</strong></div>
              <button
                class="license-key-toggle"
                type="button"
                :aria-label="showLicenseKey ? '隐藏激活码' : '显示激活码'"
                :title="showLicenseKey ? '隐藏激活码' : '显示激活码'"
                @click="showLicenseKey = !showLicenseKey"
              >
                <EyeOff v-if="showLicenseKey" :size="15" />
                <Eye v-else :size="15" />
              </button>
            </div>
            <div class="license-detail">
              <ShieldCheck :size="15" />
              <div><span>授权类型</span><strong>{{ licenseStatus?.is_permanent ? '永久授权' : `剩余 ${licenseStatus?.days_left ?? 0} 天` }}</strong></div>
            </div>
          </div>

          <div class="license-action-row">
            <p>准备换电脑时，可先释放本机名额，再在新设备使用原激活码。</p>
            <button v-if="!showDeactivateConfirm" class="btn-unlink" @click="showDeactivateConfirm = true">
              <Unlink :size="14" />解绑本机
            </button>
          </div>

          <div v-if="showDeactivateConfirm" class="deactivate-confirm">
            <div>
              <strong>确认解绑当前设备？</strong>
              <p v-if="deactivateError" class="deactivate-error">{{ deactivateError }}</p>
            </div>
            <div class="confirm-actions">
              <button class="btn-secondary" :disabled="isDeactivating" @click="showDeactivateConfirm = false">取消</button>
              <button class="btn-unlink danger" :disabled="isDeactivating" @click="handleDeactivate">
                {{ isDeactivating ? '正在解绑…' : '确认解绑' }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.settings-page {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 10px 0 30px 0;
  width: 100%;
}

.settings-container {
  display: flex;
  flex-direction: column;
  gap: 16px;
  width: 100%;
  max-width: 820px;
}

.settings-card {
  background: var(--bg-card);
  border: 1px solid var(--border-hairline);
  border-radius: var(--radius-md);
  box-shadow: var(--surface-highlight), var(--shadow-sm);
  overflow: hidden;
}

.card-header {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 16px 20px 8px 20px;
}

.card-icon-box {
  width: 28px;
  height: 28px;
  border-radius: 8px;
  background: rgba(244, 102, 143, 0.12);
  color: var(--bili-pink);
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.header-text {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.card-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
}

.card-subtitle {
  font-size: 11.5px;
  color: var(--text-muted);
}

.card-body {
  padding: 8px 20px 20px 20px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.form-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.item-label {
  font-size: 12px;
  color: var(--text-secondary);
  font-weight: 500;
}

.dir-group {
  display: flex;
  align-items: center;
  gap: 8px;
}

.speed-limit-item {
  margin-top: 2px;
  padding: 12px 14px;
  border-radius: 10px;
  background: var(--neutral-03);
  gap: 12px;
}
.speed-limit-header { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.speed-limit-header .item-label { display: block; margin-bottom: 2px; color: var(--text-primary); font-size: 13px; }
.speed-limit-controls { display: flex; flex-direction: column; gap: 6px; }
.speed-limit-unit { width: 96px; flex-shrink: 0; }
.switch-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.switch-row-label { font-size: 13px; color: var(--text-primary); }
.speed-switch { position: relative; display: inline-flex; flex-shrink: 0; width: 36px; height: 21px; cursor: pointer; }
.speed-switch input { position: absolute; opacity: 0; width: 100%; height: 100%; margin: 0; cursor: pointer; }
.speed-switch-track { width: 100%; height: 100%; border-radius: 999px; background: var(--neutral-12); pointer-events: none; transition: background .18s; }
.speed-switch-track::after { content: ''; position: absolute; top: 3px; left: 3px; width: 15px; height: 15px; border-radius: 50%; background: var(--text-muted); transition: transform .18s, background .18s; }
.speed-switch input:checked + .speed-switch-track { background: var(--bili-pink); }
.speed-switch input:checked + .speed-switch-track::after { transform: translateX(15px); background: white; }
.speed-switch input:focus-visible + .speed-switch-track { outline: 2px solid var(--bili-pink); outline-offset: 3px; }
.speed-limit-input { display: flex; align-items: center; gap: 10px; max-width: 420px; }
.speed-limit-input input { min-width: 0; width: 140px; flex: 1; }
.speed-limit-input span { color: var(--text-secondary); font-size: 12px; }
.speed-limit-item .card-subtitle { line-height: 1.6; }

.dir-input {
  flex: 1;
  height: 34px;
  padding: 0 11px;
  font-size: 12.5px;
  color: var(--text-primary);
  background: var(--bg-control);
  border-color: var(--border-subtle);
  box-shadow: var(--surface-highlight);
}

.browse-btn, .open-btn {
  height: 34px;
  padding: 0 12px;
  flex-shrink: 0;
}

.grid-2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 14px;
}

.form-select {
  height: 34px;
  padding: 0 10px;
  font-size: 12.5px;
}

.slider-box {
  display: flex;
  align-items: center;
  gap: 12px;
}

.concurrency-range {
  flex: 1;
  accent-color: var(--bili-pink);
}

.range-val-badge {
  font-size: 12px;
  font-weight: 600;
  color: var(--bili-pink);
  background: rgba(251, 114, 153, 0.12);
  padding: 3px 10px;
  border-radius: var(--radius-full);
}

.toggle-item {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
  color: var(--text-secondary);
  cursor: pointer;
}

.toggle-item input {
  accent-color: var(--bili-pink);
  width: 15px;
  height: 15px;
}

.auto-save-status { color: var(--text-muted); font-size: 11.5px; text-align: right; }
.limit-error { color: var(--danger); font-size: 11.5px; }

.license-icon-box { background: rgba(16, 185, 129, 0.12); color: var(--success); }
.license-status-badge { margin-left: auto; padding: 4px 10px; border-radius: var(--radius-full); color: var(--success); background: rgba(16, 185, 129, 0.12); font-size: 11px; font-weight: 700; }
.license-body { gap: 16px; }
.license-summary { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.license-detail { display: flex; align-items: center; gap: 10px; min-width: 0; padding: 11px 12px; border-radius: var(--radius-sm); background: var(--neutral-035); color: var(--text-muted); }
.license-detail div { min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.license-detail span { font-size: 10.5px; color: var(--text-muted); }
.license-detail strong { overflow: hidden; color: var(--text-primary); font-size: 12.5px; text-overflow: ellipsis; white-space: nowrap; }
.license-key-value { flex: 1; min-width: 0; }
.selectable-license-key { user-select: text; -webkit-user-select: text; }
.license-key-toggle { flex-shrink: 0; padding: 5px; color: var(--text-muted); background: transparent; }
.license-key-toggle:hover { color: var(--text-primary); background: var(--neutral-08); }
.license-action-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding-top: 2px; }
.license-action-row p, .deactivate-confirm p { color: var(--text-muted); font-size: 11.5px; line-height: 1.55; }
.btn-unlink { flex-shrink: 0; padding: 8px 13px; border: 1px solid rgba(239,68,68,.25); background: rgba(239,68,68,.08); color: var(--danger); }
.btn-unlink:hover { background: rgba(239,68,68,.15); }
.btn-unlink.danger { background: #dc2626; border-color: #dc2626; color: #fff; }
.deactivate-confirm { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 12px 14px; border: 1px solid rgba(239,68,68,.2); border-radius: var(--radius-sm); background: rgba(239,68,68,.055); }
.deactivate-confirm strong { display: block; margin-bottom: 3px; color: var(--text-primary); font-size: 12.5px; }
.confirm-actions { display: flex; gap: 8px; flex-shrink: 0; }
.deactivate-error { margin-top: 5px; color: var(--danger) !important; }

@media (max-width: 700px) {
  .license-summary { grid-template-columns: 1fr; }
  .license-action-row, .deactivate-confirm { align-items: flex-start; flex-direction: column; }
}
</style>
