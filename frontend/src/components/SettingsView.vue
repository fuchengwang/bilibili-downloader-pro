<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import {
  Folder,
  FolderOpen,
  Cpu,
  Save,
  ClipboardCheck,
  Check
} from 'lucide-vue-next'
import { config } from '../../wailsjs/go/models'
import {
  GetSettings,
  SaveSettings,
  SelectDirectory,
  OpenDirectory
} from '../../wailsjs/go/main/App'

const props = defineProps<{
  initialSettings: config.Settings
}>()

const emit = defineEmits<{
  (e: 'settings-saved', s: config.Settings): void
  (e: 'show-toast', msg: string, type: 'success' | 'error' | 'info'): void
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
</style>
