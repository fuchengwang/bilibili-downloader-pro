<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import QRCode from 'qrcode'
import {
  X,
  QrCode,
  Globe,
  KeyRound,
  LogOut,
  RotateCw,
  Crown,
  Coins,
  ShieldCheck,
  Smartphone,
  Sparkles,
  HelpCircle,
  CheckCircle2
} from 'lucide-vue-next'
import { bilibili } from '../../wailsjs/go/models'
import {
  GenerateQRCode,
  PollQRCode,
  GetUserInfo,
  Logout,
  SaveRawCookie
} from '../../wailsjs/go/main/App'

const props = defineProps<{
  userInfo: bilibili.UserInfo | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'login-success', user: bilibili.UserInfo): void
  (e: 'logout-success'): void
  (e: 'show-toast', msg: string, type: 'success' | 'error' | 'info'): void
}>()

const activeTab = ref<'qr' | 'web' | 'cookie'>('qr')
const qrCodeDataUrl = ref('')
const qrStatusText = ref('正在生成二维码...')
const isQrExpired = ref(false)
const isQrLoading = ref(false)
const manualCookie = ref('')
const isSavingCookie = ref(false)
const iframeKey = ref(0)
const isCheckingWebLogin = ref(false)

let pollTimer: any = null
let currentQrKey = ''
let webLoginTimer: any = null

onMounted(() => {
  if (!props.userInfo?.isLogin) {
    initQrLogin()
  }

  // 监听跨域登录完成事件
  window.addEventListener('message', handlePostMessage)
})

onUnmounted(() => {
  stopPolling()
  stopWebPolling()
  window.removeEventListener('message', handlePostMessage)
})

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

function stopWebPolling() {
  if (webLoginTimer) {
    clearInterval(webLoginTimer)
    webLoginTimer = null
  }
}

function switchTab(tab: 'qr' | 'web' | 'cookie') {
  activeTab.value = tab
  if (tab === 'web') {
    startWebPolling()
  } else {
    stopWebPolling()
  }
}

function refreshIframe() {
  iframeKey.value++
}

function startWebPolling() {
  stopWebPolling()
  webLoginTimer = setInterval(async () => {
    try {
      const user = await GetUserInfo()
      if (user && user.isLogin) {
        stopWebPolling()
        emit('show-toast', `登录成功，欢迎回来 ${user.uname}！`, 'success')
        emit('login-success', user)
        emit('close')
      }
    } catch (e) {
      // ignore
    }
  }, 2500)
}

// 监听登录完成 postMessage
async function handlePostMessage(event: MessageEvent) {
  try {
    if (event.data && (event.data.type === 'bili_login_success' || event.data.code === 0)) {
      await handleCheckWebLogin()
    }
  } catch (e) {
    // ignore
  }
}

// 初始化 B 站官方扫码登录
async function initQrLogin() {
  stopPolling()
  isQrLoading.value = true
  isQrExpired.value = false
  qrStatusText.value = '正在生成二维码...'

  try {
    const info = await GenerateQRCode()
    if (!info || !info.url || !info.qrcodeKey) {
      throw new Error('未获取到二维码数据')
    }

    currentQrKey = info.qrcodeKey
    qrCodeDataUrl.value = await QRCode.toDataURL(info.url, {
      width: 200,
      margin: 2,
      color: {
        dark: '#0f172a',
        light: '#ffffff'
      }
    })

    qrStatusText.value = '请使用手机 B 站 App 扫码'
    isQrLoading.value = false

    // 开始轮询扫码结果 (每 1.5s 一次)
    pollTimer = setInterval(pollQrStatus, 1500)
  } catch (err: any) {
    isQrLoading.value = false
    qrStatusText.value = '生成二维码失败，请点击重试'
    emit('show-toast', '生成二维码失败: ' + err.message, 'error')
  }
}

// 轮询二维码扫码状态
async function pollQrStatus() {
  if (!currentQrKey) return

  try {
    const res = await PollQRCode(currentQrKey)
    if (!res) return

    if (res.isSuccess) {
      stopPolling()
      qrStatusText.value = '登录成功！正在加载个人信息...'
      emit('show-toast', '登录成功！', 'success')
      
      const user = await GetUserInfo()
      if (user && user.isLogin) {
        emit('login-success', user)
      }
      setTimeout(() => {
        emit('close')
      }, 500)
      return
    }

    if (res.isExpired) {
      stopPolling()
      isQrExpired.value = true
      qrStatusText.value = '二维码已过期，请点击刷新'
      return
    }

    if (res.message) {
      qrStatusText.value = res.message
    }
  } catch (err) {
    // 轮询偶发网络错误忽略
  }
}

// 检查网页登录状态
async function handleCheckWebLogin() {
  isCheckingWebLogin.value = true
  try {
    const user = await GetUserInfo()
    if (user && user.isLogin) {
      stopWebPolling()
      emit('show-toast', `登录成功，欢迎回来 ${user.uname}！`, 'success')
      emit('login-success', user)
      emit('close')
    } else {
      emit('show-toast', '未检测到登录状态，请在上方完成登录后再试', 'info')
    }
  } catch (err: any) {
    emit('show-toast', '验证登录状态失败: ' + err.message, 'error')
  } finally {
    isCheckingWebLogin.value = false
  }
}

// 手动保存 Cookie
async function handleSaveCookie() {
  const c = manualCookie.value.trim()
  if (!c) {
    emit('show-toast', '请输入 Cookie 或 SESSDATA', 'error')
    return
  }

  isSavingCookie.value = true
  try {
    await SaveRawCookie(c)
    const user = await GetUserInfo()
    if (user && user.isLogin) {
      emit('show-toast', `登录成功，欢迎回来 ${user.uname}！`, 'success')
      emit('login-success', user)
      emit('close')
    } else {
      emit('show-toast', 'Cookie 校验失败，请检查是否填写正确或已过期', 'error')
    }
  } catch (err: any) {
    emit('show-toast', '保存 Cookie 失败: ' + err.message, 'error')
  } finally {
    isSavingCookie.value = false
  }
}

// 退出登录
async function handleLogout() {
  try {
    await Logout()
    emit('logout-success')
    emit('show-toast', '已退出当前账号', 'info')
    emit('close')
  } catch (err: any) {
    emit('show-toast', '退出登录失败: ' + err.message, 'error')
  }
}
</script>

<template>
  <div class="modal-overlay" @click.self="emit('close')">
    <div class="modal-content login-modal" :class="{ 'modal-wide': activeTab === 'web' }">
      <!-- Modal Header -->
      <div class="modal-header">
        <div class="header-title-box">
          <ShieldCheck class="header-icon" :size="20" />
          <h2 class="title-text">哔哩哔哩账号登录</h2>
        </div>
        <button class="btn-icon close-btn" @click="emit('close')" title="关闭">
          <X :size="18" />
        </button>
      </div>

      <!-- State A: Already Logged In Profile View -->
      <div v-if="userInfo && userInfo.isLogin" class="profile-body">
        <div class="user-card">
          <div class="avatar-wrap">
            <img :src="userInfo.face" class="user-avatar" alt="Avatar" />
            <span class="user-level-badge">LV{{ userInfo.level }}</span>
          </div>

          <div class="user-main-info">
            <div class="uname-row">
              <span class="uname">{{ userInfo.uname }}</span>
              <span v-if="userInfo.vipStatus === 1" class="vip-badge">
                <Crown :size="12" />
                <span>{{ userInfo.vipLabel || '大会员' }}</span>
              </span>
            </div>
            <div class="uid-text">UID: {{ userInfo.mid }}</div>
            <div v-if="userInfo.vipDueStr" class="vip-expire-text">
              大会员有效期至: {{ userInfo.vipDueStr }}
            </div>
          </div>
        </div>

        <div class="profile-stats-grid">
          <div class="stat-card">
            <Coins :size="16" class="stat-icon coin-icon" />
            <div class="stat-info">
              <span class="stat-label">硬币数</span>
              <span class="stat-val">{{ userInfo.money.toFixed(1) }}</span>
            </div>
          </div>
          <div class="stat-card">
            <Sparkles :size="16" class="stat-icon vip-icon" />
            <div class="stat-info">
              <span class="stat-label">账号特权</span>
              <span class="stat-val">{{ userInfo.vipStatus === 1 ? '已解锁 8K/4K/1080P60' : '可下载 1080P 高清' }}</span>
            </div>
          </div>
        </div>

        <div class="profile-footer">
          <button class="btn-secondary logout-btn" @click="handleLogout">
            <LogOut :size="14" />
            <span>退出当前账号</span>
          </button>
        </div>
      </div>

      <!-- State B: Login Forms -->
      <div v-else class="login-body">
        <!-- Tabs -->
        <div class="login-tabs">
          <button
            class="tab-btn"
            :class="{ active: activeTab === 'qr' }"
            @click="switchTab('qr')"
          >
            <QrCode :size="15" />
            <span>扫码登录 (推荐)</span>
          </button>

          <button
            class="tab-btn"
            :class="{ active: activeTab === 'web' }"
            @click="switchTab('web')"
          >
            <Globe :size="15" />
            <span>内置浏览器登录</span>
          </button>

          <button
            class="tab-btn"
            :class="{ active: activeTab === 'cookie' }"
            @click="switchTab('cookie')"
          >
            <KeyRound :size="15" />
            <span>填入 Cookie</span>
          </button>
        </div>

        <!-- Tab 1: QR Code Scan Login (Official, Safe, 100% Reliable) -->
        <div v-show="activeTab === 'qr'" class="tab-pane qr-pane">
          <div class="qr-box" @click="isQrExpired ? initQrLogin() : null">
            <img v-if="qrCodeDataUrl && !isQrLoading" :src="qrCodeDataUrl" class="qr-image" alt="QR Code" />
            
            <div v-if="isQrLoading" class="qr-loading-mask">
              <RotateCw :size="24" class="spin-icon" />
              <span>正在生成二维码...</span>
            </div>

            <div v-if="isQrExpired" class="qr-expired-mask">
              <RotateCw :size="28" />
              <span>二维码已过期</span>
              <span class="refresh-hint">点击重新获取</span>
            </div>
          </div>

          <div class="qr-status-box">
            <Smartphone :size="14" class="status-device-icon" />
            <p class="qr-status-text">{{ qrStatusText }}</p>
          </div>

          <div class="qr-tips">
            打开手机哔哩哔哩 App > 首页右上角「扫一扫」> 点击确认登录即可
          </div>
        </div>

        <!-- Tab 2: Built-in Browser Login View (Spacious & Clean) -->
        <div v-show="activeTab === 'web'" class="tab-pane web-pane">
          <div class="browser-window-frame">
            <div class="browser-toolbar">
              <div class="window-dots">
                <span class="dot dot-red"></span>
                <span class="dot dot-yellow"></span>
                <span class="dot dot-green"></span>
              </div>
              <div class="browser-address">
                <ShieldCheck :size="13" class="secure-icon" />
                <span class="url-text">https://passport.bilibili.com/login</span>
              </div>
              <button class="btn-icon refresh-btn" @click="refreshIframe" title="刷新页面">
                <RotateCw :size="12" />
              </button>
            </div>

            <div class="iframe-container">
              <iframe
                :key="iframeKey"
                src="https://passport.bilibili.com/login"
                class="web-login-iframe"
                allow="camera; microphone; geolocation; encrypted-media; clipboard-read; clipboard-write;"
              ></iframe>
            </div>
          </div>

          <div class="web-footer">
            <button
              class="btn-primary web-sync-btn"
              :disabled="isCheckingWebLogin"
              @click="handleCheckWebLogin"
            >
              <RotateCw v-if="isCheckingWebLogin" :size="14" class="spin-icon" />
              <CheckCircle2 v-else :size="14" />
              <span>{{ isCheckingWebLogin ? '正在检测登录态...' : '我已在上方完成登录，立即同步' }}</span>
            </button>
          </div>
        </div>

        <!-- Tab 3: Manual Cookie Paste -->
        <div v-show="activeTab === 'cookie'" class="tab-pane cookie-pane">
          <div class="form-item">
            <label class="item-label">
              粘贴 B 站 Cookie 或 SESSDATA:
            </label>
            <textarea
              v-model="manualCookie"
              class="cookie-textarea"
              placeholder="例如直接粘贴: SESSDATA=xxx; 或整段浏览器 Cookie 字符串"
            ></textarea>
          </div>

          <div class="cookie-help">
            <HelpCircle :size="14" class="help-icon" />
            <span>提示: 浏览器按 F12 > 应用 (Application) > Cookie > 找到 SESSDATA 复制其值粘贴即可</span>
          </div>

          <div class="cookie-actions">
            <button
              class="btn-primary cookie-save-btn"
              :disabled="isSavingCookie"
              @click="handleSaveCookie"
            >
              <RotateCw v-if="isSavingCookie" :size="14" class="spin-icon" />
              <span>{{ isSavingCookie ? '正在验证...' : '保存并登录' }}</span>
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.login-modal {
  width: 440px;
  max-width: 95vw;
  max-height: 92vh;
  overflow-y: auto;
  transition: width 0.25s cubic-bezier(0.4, 0, 0.2, 1);
}

.modal-wide {
  width: 860px;
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border-subtle);
}

.header-title-box {
  display: flex;
  align-items: center;
  gap: 8px;
}

.header-icon {
  color: var(--primary);
}

.title-text {
  font-size: 16px;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0;
}

.close-btn {
  color: var(--text-muted);
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 4px;
  border-radius: var(--radius-sm);
  background: transparent;
  border: none;
  transition: all var(--transition-fast);
}

.close-btn:hover {
  color: var(--text-primary);
  background: var(--bg-hover);
}

/* User Profile Card */
.profile-body {
  padding: 24px 20px;
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.user-card {
  display: flex;
  align-items: center;
  gap: 16px;
  background: var(--bg-tertiary);
  padding: 16px;
  border-radius: var(--radius-md);
  border: 1px solid var(--border-subtle);
}

.avatar-wrap {
  position: relative;
  width: 56px;
  height: 56px;
  flex-shrink: 0;
}

.user-avatar {
  width: 100%;
  height: 100%;
  border-radius: 50%;
  object-fit: cover;
  border: 2px solid var(--border-subtle);
}

.user-level-badge {
  position: absolute;
  bottom: -2px;
  right: -2px;
  background: var(--primary);
  color: #fff;
  font-size: 10px;
  font-weight: 700;
  padding: 1px 4px;
  border-radius: 4px;
  border: 1px solid var(--bg-card);
}

.user-main-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
  overflow: hidden;
}

.uname-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.uname {
  font-size: 16px;
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.vip-badge {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  background: linear-gradient(135deg, #fb7299, #ff5722);
  color: #fff;
  font-size: 10px;
  font-weight: 600;
  padding: 2px 6px;
  border-radius: 4px;
  flex-shrink: 0;
}

.uid-text {
  font-size: 12px;
  color: var(--text-muted);
}

.vip-expire-text {
  font-size: 11px;
  color: var(--text-secondary);
}

.profile-stats-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
}

.stat-card {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
}

.stat-icon {
  flex-shrink: 0;
}

.coin-icon {
  color: #f59e0b;
}

.vip-icon {
  color: var(--primary);
}

.stat-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
  overflow: hidden;
}

.stat-label {
  font-size: 11px;
  color: var(--text-muted);
}

.stat-val {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.profile-footer {
  display: flex;
  justify-content: flex-end;
}

.logout-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 16px;
  font-size: 13px;
  color: var(--text-secondary);
}

.logout-btn:hover {
  color: #ef4444;
  border-color: rgba(239, 68, 68, 0.3);
  background: rgba(239, 68, 68, 0.05);
}

/* Login Tabs */
.login-body {
  padding: 16px 20px;
}

.login-tabs {
  display: flex;
  gap: 8px;
  background: var(--bg-tertiary);
  padding: 4px;
  border-radius: var(--radius-sm);
  margin-bottom: 12px;
}

.tab-btn {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 8px 12px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-muted);
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: all var(--transition-fast);
}

.tab-btn:hover {
  color: var(--text-primary);
}

.tab-btn.active {
  background: var(--bg-card);
  color: var(--primary);
  font-weight: 600;
  box-shadow: var(--shadow-sm);
}

.tab-pane {
  display: flex;
  flex-direction: column;
  align-items: center;
}

/* Tab 1: QR Pane */
.qr-box {
  position: relative;
  width: 190px;
  height: 190px;
  background: #ffffff;
  padding: 10px;
  border-radius: var(--radius-md);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  overflow: hidden;
}

.qr-image {
  width: 100%;
  height: 100%;
  display: block;
}

.qr-loading-mask,
.qr-expired-mask {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(15, 23, 42, 0.85);
  color: #fff;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  font-size: 13px;
  text-align: center;
  backdrop-filter: blur(2px);
}

.refresh-hint {
  font-size: 11px;
  color: rgba(255, 255, 255, 0.7);
}

.spin-icon {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

.qr-status-box {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 14px;
  margin-bottom: 6px;
}

.status-device-icon {
  color: var(--primary);
}

.qr-status-text {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-primary);
  margin: 0;
}

.qr-tips {
  font-size: 12px;
  color: var(--text-muted);
  text-align: center;
  line-height: 1.5;
  margin-top: 4px;
}

/* Tab 2: Built-in Browser Window Frame */
.web-pane {
  width: 100%;
}

.browser-window-frame {
  width: 100%;
  border-radius: var(--radius-md);
  border: 1px solid var(--border-subtle);
  overflow: hidden;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
  background: #ffffff;
}

.browser-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: #1e293b;
  padding: 8px 12px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.1);
}

.window-dots {
  display: flex;
  align-items: center;
  gap: 6px;
}

.dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
}

.dot-red { background: #ef4444; }
.dot-yellow { background: #f59e0b; }
.dot-green { background: #10b981; }

.browser-address {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  color: #94a3b8;
  background: rgba(0, 0, 0, 0.3);
  padding: 3px 12px;
  border-radius: 12px;
}

.secure-icon {
  color: #10b981;
}

.refresh-btn {
  color: #94a3b8;
  padding: 3px;
}

.refresh-btn:hover {
  color: #ffffff;
}

.iframe-container {
  width: 100%;
  height: 410px;
  background: #ffffff;
}

.web-login-iframe {
  width: 100%;
  height: 100%;
  border: none;
  display: block;
}

.web-footer {
  display: flex;
  justify-content: center;
  margin-top: 12px;
}

.web-sync-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 9px 24px;
  font-size: 13px;
  font-weight: 600;
}

/* Tab 3: Cookie Pane */
.cookie-pane {
  align-items: stretch;
}

.form-item {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
}

.item-label {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-secondary);
}

.cookie-textarea {
  width: 100%;
  height: 100px;
  padding: 10px;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  color: var(--text-primary);
  font-size: 12px;
  font-family: monospace;
  resize: vertical;
  outline: none;
  transition: border-color var(--transition-fast);
}

.cookie-textarea:focus {
  border-color: var(--primary);
}

.cookie-help {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin-top: 10px;
  font-size: 11px;
  color: var(--text-muted);
  line-height: 1.4;
}

.help-icon {
  flex-shrink: 0;
  margin-top: 1px;
}

.cookie-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}

.cookie-save-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 8px 18px;
  font-size: 13px;
}
</style>
