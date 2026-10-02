<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import {
  Folder,
  FolderOpen,
  Cpu,
  Save,
  ClipboardCheck,
  ShieldCheck,
  KeyRound,
  Unlink,
  Eye,
  EyeOff
} from 'lucide-vue-next'
import { config, license } from '../../wailsjs/go/models'
import {
  GetSettings,
  SaveSettings,
  SelectDirectory,
  OpenDirectory,
  DeactivateLicense
} from '../../wailsjs/go/main/App'

const props = defineProps<{
  initialSettings: config.Settings
  licenseStatus: license.LicenseStatus | null
}>()

const emit = defineEmits<{
  (e: 'settings-saved', s: config.Settings): void
  (e: 'show-toast', msg: string, type: 'success' | 'error' | 'info'): void
  (e: 'license-deactivated'): void
}>()

const form = ref<config.Settings>({
  downloadDir: props.initialSettings?.downloadDir || '',
  defaultQuality: props.initialSettings?.defaultQuality || 'highest',
  defaultCodec: props.initialSettings?.defaultCodec || 'auto',
  maxConcurrent: props.initialSettings?.maxConcurrent || 3,
  threadsPerTask: props.initialSettings?.threadsPerTask || 4,
  autoMerge: props.initialSettings?.autoMerge ?? true,
  deleteTempFiles: props.initialSettings?.deleteTempFiles ?? true,
  autoClipboard: props.initialSettings?.autoClipboard ?? true,
  fileNameTemplate: props.initialSettings?.fileNameTemplate || '{title} - {part}',
  theme: props.initialSettings?.theme || 'dark'
})

const isSaving = ref(false)
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
    emit('license-deactivated')
  } catch (err: any) {
    const message = err?.message || String(err)
    deactivateError.value = /connect|network|timeout|连接|网络/i.test(message)
      ? '无法连接激活服务器，当前授权仍保留，请检查网络后重试'
      : message
  } finally {
    isDeactivating.value = false
  }
}

// 响应父级设置变更
watch(
  () => props.initialSettings,
  (val) => {
    if (val && val.downloadDir) {
      form.value = { ...form.value, ...val }
    }
  },
  { immediate: true, deep: true }
)

onMounted(async () => {
  try {
    const s = await GetSettings()
    if (s) {
      form.value = { ...form.value, ...s }
    }
  } catch (e) {}
})

async function handleBrowseDir() {
  try {
    const path = await SelectDirectory()
    if (path) {
      form.value.downloadDir = path
      emit('show-toast', '已成功更改下载目录', 'success')
      emit('settings-saved', form.value)
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
  isSaving.value = true
  try {
    await SaveSettings(form.value)
    emit('show-toast', '偏好设置已成功保存！', 'success')
    emit('settings-saved', form.value)
  } catch (err: any) {
    emit('show-toast', '保存设置失败: ' + err.message, 'error')
  } finally {
    isSaving.value = false
  }
}
</script>

<template>
  <div class="settings-page">
    <div class="settings-container">
      <!-- Section 1: 下载目录 -->
      <div class="settings-card">
        <div class="card-header">
          <div class="card-icon-box">
            <Folder :size="18" />
          </div>
          <div class="header-text">
            <h3 class="card-title">文件存储位置</h3>
            <p class="card-subtitle">视频下载完成后将自动保存至此目录</p>
          </div>
        </div>

        <div class="card-body">
          <div class="form-item">
            <label class="item-label">默认下载路径</label>
            <div class="dir-group">
              <input
                v-model="form.downloadDir"
                type="text"
                class="dir-input"
                placeholder="正在获取默认下载目录..."
              />
              <button class="btn-secondary browse-btn" @click="handleBrowseDir">
                <span>更改目录...</span>
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
            <Cpu :size="18" />
          </div>
          <div class="header-text">
            <h3 class="card-title">下载与画质偏好</h3>
            <p class="card-subtitle">设置默认下载画质、编码与并发任务数量</p>
          </div>
        </div>

        <div class="card-body">
          <div class="grid-2">
            <div class="form-item">
              <label class="item-label">默认首选画质</label>
              <select v-model="form.defaultQuality" class="form-select">
                <option value="highest">最高画质 (自动匹配当前账号最高权限)</option>
                <option value="127">8K 超高清</option>
                <option value="120">4K 超清</option>
                <option value="116">1080P 60帧</option>
                <option value="112">1080P 高码率</option>
                <option value="80">1080P 高清</option>
                <option value="64">720P 高清</option>
                <option value="32">480P 清晰</option>
              </select>
            </div>

            <div class="form-item">
              <label class="item-label">默认视频编码</label>
              <select v-model="form.defaultCodec" class="form-select">
                <option value="auto">智能优选 (自动推荐)</option>
                <option value="AVC">AVC / H.264 (兼容性最好)</option>
                <option value="HEVC">HEVC / H.265 (高压缩比)</option>
                <option value="AV1">AV1 (极速高画质)</option>
              </select>
            </div>
          </div>

          <div class="grid-2" style="margin-top: 14px;">
            <div class="form-item">
              <label class="item-label">单任务分片下载线程数</label>
              <select v-model.number="form.threadsPerTask" class="form-select">
                <option :value="1">1 线程 (单流平稳)</option>
                <option :value="2">2 线程</option>
                <option :value="3">3 线程</option>
                <option :value="4">4 线程 (推荐)</option>
                <option :value="6">6 线程</option>
                <option :value="8">8 线程 (极速加速)</option>
              </select>
            </div>

            <div class="form-item">
              <label class="item-label">最大同时下载任务数</label>
              <div class="slider-box">
                <input
                  v-model.number="form.maxConcurrent"
                  type="range"
                  min="1"
                  max="10"
                  class="concurrency-range"
                />
                <span class="range-val-badge">{{ form.maxConcurrent }} 个任务</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Section 3: 剪贴板感应与交互 -->
      <div class="settings-card">
        <div class="card-header">
          <div class="card-icon-box">
            <ClipboardCheck :size="18" />
          </div>
          <div class="header-text">
            <h3 class="card-title">系统交互与感应</h3>
            <p class="card-subtitle">智能识别剪贴板视频链接</p>
          </div>
        </div>

        <div class="card-body">
          <label class="toggle-item">
            <input v-model="form.autoClipboard" type="checkbox" />
            <span class="toggle-text">开启剪贴板自动感应 (切换到应用时若剪贴板为 B 站链接则自动填入并直接解析)</span>
          </label>
        </div>
      </div>

      <!-- Bottom Save Action Bar -->
      <div class="save-bar">
        <button class="btn-primary main-save-btn" :disabled="isSaving" @click="handleSave">
          <Save :size="15" />
          <span>{{ isSaving ? '正在保存...' : '保存全部偏好设置' }}</span>
        </button>
      </div>

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
              <div class="license-key-value"><span>激活码</span><strong>{{ showLicenseKey ? (licenseStatus?.license_key || '—') : maskLicenseKey(licenseStatus?.license_key) }}</strong></div>
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
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-sm);
  overflow: hidden;
}

.card-header {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 18px;
  border-bottom: 1px solid var(--border-subtle);
  background: rgba(255, 255, 255, 0.015);
}

.card-icon-box {
  width: 32px;
  height: 32px;
  border-radius: var(--radius-sm);
  background: rgba(251, 114, 153, 0.12);
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
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  gap: 12px;
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

.dir-input {
  flex: 1;
  height: 34px;
  padding: 0 10px;
  font-size: 12.5px;
  color: #fff;
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

.save-bar {
  display: flex;
  justify-content: flex-end;
  padding-top: 8px;
}

.main-save-btn {
  height: 38px;
  padding: 0 24px;
  font-size: 13.5px;
  box-shadow: 0 4px 16px rgba(251, 114, 153, 0.35);
}

.license-icon-box { background: rgba(16, 185, 129, 0.12); color: var(--success); }
.license-status-badge { margin-left: auto; padding: 4px 10px; border-radius: var(--radius-full); color: var(--success); background: rgba(16, 185, 129, 0.12); font-size: 11px; font-weight: 700; }
.license-body { gap: 16px; }
.license-summary { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.license-detail { display: flex; align-items: center; gap: 10px; min-width: 0; padding: 11px 12px; border-radius: var(--radius-sm); background: rgba(255,255,255,.035); color: var(--text-muted); }
.license-detail div { min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.license-detail span { font-size: 10.5px; color: var(--text-muted); }
.license-detail strong { overflow: hidden; color: var(--text-primary); font-size: 12.5px; text-overflow: ellipsis; white-space: nowrap; }
.license-key-value { flex: 1; min-width: 0; }
.license-key-toggle { flex-shrink: 0; padding: 5px; color: var(--text-muted); background: transparent; }
.license-key-toggle:hover { color: var(--text-primary); background: rgba(255,255,255,.08); }
.license-action-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding-top: 2px; }
.license-action-row p, .deactivate-confirm p { color: var(--text-muted); font-size: 11.5px; line-height: 1.55; }
.btn-unlink { flex-shrink: 0; padding: 8px 13px; border: 1px solid rgba(239,68,68,.25); background: rgba(239,68,68,.08); color: #f87171; }
.btn-unlink:hover { background: rgba(239,68,68,.15); }
.btn-unlink.danger { background: #dc2626; border-color: #dc2626; color: #fff; }
.deactivate-confirm { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 12px 14px; border: 1px solid rgba(239,68,68,.2); border-radius: var(--radius-sm); background: rgba(239,68,68,.055); }
.deactivate-confirm strong { display: block; margin-bottom: 3px; color: var(--text-primary); font-size: 12.5px; }
.confirm-actions { display: flex; gap: 8px; flex-shrink: 0; }
.deactivate-error { margin-top: 5px; color: #f87171 !important; }

@media (max-width: 700px) {
  .license-summary { grid-template-columns: 1fr; }
  .license-action-row, .deactivate-confirm { align-items: flex-start; flex-direction: column; }
}
</style>
