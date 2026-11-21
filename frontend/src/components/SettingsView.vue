<script setup lang="ts">
import { ref, onBeforeUnmount } from 'vue'
import {
  FolderOpen,
  Unlink,
  Eye,
  EyeOff
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
  { value: 'light', label: '浅色' },
  { value: 'dark', label: '深色' },
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
      <!-- Group 1: 外观与存储 -->
      <section class="settings-section">
        <h3 class="settings-section-title">外观与存储</h3>
        <div class="settings-group">
          <div class="setting-row">
            <div class="setting-label-wrap">
              <span class="setting-label">界面模式</span>
            </div>
            <div class="setting-control">
              <div class="segmented-control" role="radiogroup" aria-label="界面模式">
                <button
                  v-for="opt in themeOptions"
                  :key="opt.value"
                  type="button"
                  role="radio"
                  class="segmented-btn"
                  :class="{ active: form.theme === opt.value }"
                  :aria-checked="form.theme === opt.value"
                  @click="() => { form.theme = opt.value; savePreference('theme', opt.value) }"
                >
                  {{ opt.label }}
                </button>
              </div>
            </div>
          </div>

          <div class="setting-row">
            <div class="setting-label-wrap">
              <span class="setting-label">下载目录</span>
            </div>
            <div class="setting-control dir-control">
              <input
                v-model="form.downloadDir"
                type="text"
                class="dir-input"
                placeholder="正在获取默认下载目录..."
                @input="scheduleDirectorySave"
                @blur="saveDirectory"
                @keydown.enter="saveDirectory"
              />
              <button class="btn-secondary browse-btn" @click="handleBrowseDir">
                <span>更改</span>
              </button>
              <button class="btn-secondary open-btn" @click="handleOpenDir" title="在访达/资源管理器中打开">
                <FolderOpen :size="14" />
              </button>
            </div>
          </div>
        </div>
      </section>

      <!-- Group 2: 下载与画质 -->
      <section class="settings-section">
        <h3 class="settings-section-title">下载与画质</h3>
        <div class="settings-group">
          <div class="setting-row">
            <div class="setting-label-wrap">
              <span class="setting-label">首选画质</span>
            </div>
            <div class="setting-control select-control">
              <AppSelect
                :model-value="form.defaultQuality"
                :options="qualityOptions"
                aria-label="首选画质"
                @update:model-value="val => { form.defaultQuality = String(val); savePreference('defaultQuality', form.defaultQuality) }"
              />
            </div>
          </div>

          <div class="setting-row">
            <div class="setting-label-wrap">
              <span class="setting-label">视频编码</span>
            </div>
            <div class="setting-control select-control">
              <AppSelect
                :model-value="form.defaultCodec"
                :options="codecOptions"
                aria-label="视频编码"
                @update:model-value="val => { form.defaultCodec = String(val); savePreference('defaultCodec', form.defaultCodec) }"
              />
            </div>
          </div>

          <div class="setting-row">
            <div class="setting-label-wrap">
              <span class="setting-label">单任务分片线程</span>
            </div>
            <div class="setting-control select-control-sm">
              <AppSelect
                :model-value="form.threadsPerTask"
                :options="threadOptions"
                aria-label="单任务分片线程"
                @update:model-value="val => { form.threadsPerTask = Number(val); savePreference('threadsPerTask', form.threadsPerTask) }"
              />
            </div>
          </div>

          <div class="setting-row">
            <div class="setting-label-wrap">
              <span class="setting-label">最大同时下载数</span>
            </div>
            <div class="setting-control slider-control">
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

          <div class="setting-row setting-row-stack">
            <div class="setting-row-main">
              <div class="setting-label-wrap">
                <label class="setting-label" for="limit-enabled">限制下载速度</label>
                <span class="setting-desc">{{ limitEnabled ? '所有任务共享上限' : '不限速' }}</span>
              </div>
              <div class="setting-control">
                <label class="speed-switch">
                  <input id="limit-enabled" v-model="limitEnabled" type="checkbox" role="switch" aria-label="限制下载速度" @change="handleLimitToggle" />
                  <span class="speed-switch-track" aria-hidden="true"></span>
                </label>
              </div>
            </div>

            <div v-if="limitEnabled" class="speed-limit-subrow">
              <div class="speed-limit-input">
                <input
                  id="download-limit"
                  v-model="downloadLimitInput"
                  type="text"
                  inputmode="decimal"
                  autocomplete="off"
                  class="dir-input"
                  aria-label="下载速度上限"
                  @input="scheduleLimitSave"
                  @blur="handleSave"
                  @keydown.enter="handleSave"
                />
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
      </section>

      <!-- Group 3: 系统交互 -->
      <section class="settings-section">
        <h3 class="settings-section-title">系统交互</h3>
        <div class="settings-group">
          <div class="setting-row">
            <div class="setting-label-wrap">
              <span class="setting-label">剪贴板自动感应</span>
              <span class="setting-desc">切回应用时自动解析剪贴板 B 站链接</span>
            </div>
            <div class="setting-control">
              <label class="speed-switch">
                <input v-model="form.autoClipboard" type="checkbox" role="switch" aria-label="自动识别剪贴板 B 站链接" @change="savePreference('autoClipboard', form.autoClipboard)" />
                <span class="speed-switch-track" aria-hidden="true"></span>
              </label>
            </div>
          </div>
        </div>
      </section>

      <!-- Group 4: 软件更新 -->
      <UpdatePanel :state="updateState" />

      <!-- Group 5: 专业版授权 -->
      <section class="settings-section">
        <div class="section-header-row">
          <h3 class="settings-section-title">专业版授权</h3>
          <span class="license-pill">
            <span class="license-dot" aria-hidden="true"></span>
            已激活
          </span>
        </div>
        <div class="settings-group">
          <div class="setting-row">
            <div class="setting-label-wrap">
              <span class="setting-label">激活码</span>
            </div>
            <div class="setting-control license-key-control">
              <strong class="license-key-text" :class="{ 'selectable-license-key': showLicenseKey }">
                {{ showLicenseKey ? (licenseStatus?.license_key || '—') : maskLicenseKey(licenseStatus?.license_key) }}
              </strong>
              <button
                class="license-key-toggle"
                type="button"
                :aria-label="showLicenseKey ? '隐藏激活码' : '显示激活码'"
                :title="showLicenseKey ? '隐藏激活码' : '显示激活码'"
                @click="showLicenseKey = !showLicenseKey"
              >
                <EyeOff v-if="showLicenseKey" :size="14" />
                <Eye v-else :size="14" />
              </button>
            </div>
          </div>

          <div class="setting-row setting-row-stack">
            <div class="setting-row-main">
              <div class="setting-label-wrap">
                <span class="setting-label">授权状态</span>
                <span class="setting-desc">{{ licenseStatus?.is_permanent ? '永久授权' : `剩余 ${licenseStatus?.days_left ?? 0} 天` }}</span>
              </div>
              <div class="setting-control">
                <button v-if="!showDeactivateConfirm" class="btn-unlink" @click="showDeactivateConfirm = true">
                  <Unlink :size="13" />解绑本机
                </button>
              </div>
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
      </section>

      <p class="auto-save-status" role="status">{{ saveStatus }}</p>
    </div>
  </div>
</template>

<style scoped>
.settings-page {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 6px 0 32px 0;
  width: 100%;
}

.settings-container {
  display: flex;
  flex-direction: column;
  gap: 22px;
  width: 100%;
  max-width: 720px;
}

.settings-section {
  display: flex;
  flex-direction: column;
}

.section-header-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 4px 8px 4px;
}

.section-header-row .settings-section-title {
  padding: 0;
}

.settings-section-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  padding: 0 4px 8px 4px;
  letter-spacing: 0.01em;
}

.settings-group {
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: 12px;
  box-shadow: var(--surface-highlight), var(--shadow-sm);
}

.setting-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  padding: 13px 16px;
}

.setting-row + .setting-row {
  border-top: 1px solid var(--border-hairline);
}

.setting-row-stack {
  flex-direction: column;
  align-items: stretch;
  gap: 12px;
}

.setting-row-main {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
}

.setting-label-wrap {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.setting-label {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-primary);
}

.setting-desc {
  font-size: 11.5px;
  color: var(--text-muted);
}

.setting-control {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  flex-shrink: 0;
}

.select-control {
  width: 210px;
}

.select-control-sm {
  width: 160px;
}

.dir-control {
  flex: 1;
  max-width: 420px;
  min-width: 0;
}

.dir-input {
  flex: 1;
  min-width: 0;
  height: 32px;
  padding: 0 10px;
  font-size: 12.5px;
  color: var(--text-primary);
  background: var(--bg-control);
  border-color: var(--border-subtle);
  box-shadow: var(--surface-highlight);
}

.browse-btn,
.open-btn {
  height: 32px;
  padding: 0 11px;
  font-size: 12px;
  flex-shrink: 0;
}

.open-btn {
  width: 32px;
  padding: 0;
}

/* macOS Segmented Control */
.segmented-control {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 3px;
  border-radius: 9px;
  background: var(--bg-app);
  border: 1px solid var(--border-hairline);
}

.segmented-btn {
  height: 26px;
  padding: 0 12px;
  border-radius: 6px;
  background: transparent;
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 500;
  transition: background var(--transition-fast), color var(--transition-fast), box-shadow var(--transition-fast);
}

.segmented-btn:hover:not(.active) {
  color: var(--text-primary);
}

.segmented-btn.active {
  background: var(--bg-control-hover);
  color: var(--text-primary);
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.35), inset 0 1px 0 rgba(255, 255, 255, 0.06);
}

/* Slider */
.slider-control {
  gap: 12px;
}

.concurrency-range {
  width: 140px;
  accent-color: var(--bili-pink);
}

.range-val-badge {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-primary);
  background: var(--bg-control);
  padding: 3px 10px;
  border-radius: var(--radius-full);
  font-variant-numeric: tabular-nums;
}

/* Toggle Switch */
.speed-switch {
  position: relative;
  display: inline-flex;
  flex-shrink: 0;
  width: 36px;
  height: 20px;
  cursor: pointer;
}

.speed-switch input {
  position: absolute;
  opacity: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  cursor: pointer;
}

.speed-switch-track {
  width: 100%;
  height: 100%;
  border-radius: 999px;
  background: var(--neutral-12);
  pointer-events: none;
  transition: background 0.18s;
}

.speed-switch-track::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: var(--text-secondary);
  transition: transform 0.18s, background 0.18s;
}

.speed-switch input:checked + .speed-switch-track {
  background: var(--bili-pink);
}

.speed-switch input:checked + .speed-switch-track::after {
  transform: translateX(16px);
  background: #ffffff;
}

.speed-switch input:focus-visible + .speed-switch-track {
  outline: 2px solid var(--bili-pink);
  outline-offset: 3px;
}

/* Speed Limit Subrow */
.speed-limit-subrow {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 6px;
}

.speed-limit-input {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 240px;
}

.speed-limit-input input {
  min-width: 0;
  flex: 1;
}

.speed-limit-unit {
  width: 92px;
  flex-shrink: 0;
}

.limit-error {
  color: var(--danger);
  font-size: 11.5px;
}

/* License */
.license-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11.5px;
  font-weight: 500;
  color: var(--success);
}

.license-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--success);
}

.license-key-control {
  gap: 6px;
}

.license-key-text {
  font-size: 12.5px;
  font-weight: 500;
  color: var(--text-secondary);
  font-variant-numeric: tabular-nums;
}

.selectable-license-key {
  user-select: text;
  -webkit-user-select: text;
}

.license-key-toggle {
  padding: 5px;
  color: var(--text-muted);
  background: transparent;
}

.license-key-toggle:hover {
  color: var(--text-primary);
  background: var(--neutral-08);
}

.btn-unlink {
  height: 30px;
  padding: 0 11px;
  font-size: 12px;
  border: 1px solid rgba(239, 68, 68, 0.25);
  background: rgba(239, 68, 68, 0.08);
  color: var(--danger);
}

.btn-unlink:hover {
  background: rgba(239, 68, 68, 0.15);
}

.btn-unlink.danger {
  background: #dc2626;
  border-color: #dc2626;
  color: #fff;
}

.deactivate-confirm {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 10px 12px;
  border: 1px solid rgba(239, 68, 68, 0.2);
  border-radius: var(--radius-sm);
  background: rgba(239, 68, 68, 0.055);
}

.deactivate-confirm strong {
  display: block;
  color: var(--text-primary);
  font-size: 12.5px;
}

.deactivate-confirm p {
  font-size: 11.5px;
  color: var(--text-muted);
}

.confirm-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}

.deactivate-error {
  margin-top: 4px;
  color: var(--danger) !important;
}

.auto-save-status {
  color: var(--text-muted);
  font-size: 11.5px;
  text-align: right;
  padding-right: 4px;
}

@media (max-width: 640px) {
  .setting-row,
  .setting-row-main {
    flex-direction: column;
    align-items: flex-start;
    gap: 10px;
  }

  .setting-control,
  .dir-control,
  .select-control,
  .select-control-sm,
  .speed-limit-subrow {
    width: 100%;
    max-width: none;
    justify-content: flex-start;
    align-items: flex-start;
  }

  .deactivate-confirm {
    flex-direction: column;
    align-items: flex-start;
  }
}
</style>
