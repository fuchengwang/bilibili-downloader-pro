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
  ExternalLink,
  Smartphone,
  Sparkles,
  HelpCircle,
  CheckCircle2
} from 'lucide-vue-next'
import { bilibili } from '../../wailsjs/go/models'
import {
  GenerateQRCode,
  PollQRCode,
  OpenBrowserLogin,
  ExtractBrowserCookies,
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

const activeTab = ref<'qr' | 'browser' | 'cookie'>('qr')
const qrCodeDataUrl = ref('')
const qrStatusText = ref('正在生成二维码...')
const isQrExpired = ref(false)
const isQrLoading = ref(false)
const isSyncingBrowser = ref(false)
const manualCookie = ref('')
const isSavingCookie = ref(false)

let pollTimer: any = null
let currentQrKey = ''

onMounted(() => {
  if (!props.userInfo?.isLogin) {
    initQrLogin()
  }
})

onUnmounted(() => {
  stopPolling()
})

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

function normalizeImg(url?: string) {
  if (!url) return ''
  if (url.startsWith('//')) return 'https:' + url
  if (url.startsWith('http://')) return 'https://' + url.substring(7)
  return url
}

// 初始化/刷新二维码
async function initQrLogin() {
  stopPolling()
  isQrLoading.value = true
  isQrExpired.value = false
  qrStatusText.value = '正在获取登录二维码...'

  try {
    const res = await GenerateQRCode()
    if (!res || !res.url || !res.qrcodeKey) {
      throw new Error('获取二维码信息失败')
    }

    currentQrKey = res.qrcodeKey
    // 生成高清二维码 Data URL
    qrCodeDataUrl.value = await QRCode.toDataURL(res.url, {
      width: 220,
      margin: 1,
      color: {
        dark: '#000000',
        light: '#ffffff',
      },
    })

    qrStatusText.value = '请打开哔哩哔哩手机 App 扫码'
    isQrLoading.value = false

    // 开始轮询 (每 2 秒一次)
    pollTimer = setInterval(checkQrStatus, 2000)
  } catch (err: any) {
    isQrLoading.value = false
    qrStatusText.value = '生成二维码失败，请点击刷新'
    isQrExpired.value = true
  }
}

// 轮询检查扫码状态
async function checkQrStatus() {
  if (!currentQrKey) return

  try {
    const res = await PollQRCode(currentQrKey)
    if (!res) return

    if (res.code === 0 && res.isSuccess) {
      // 登录成功
      stopPolling()
      qrStatusText.value = '登录成功！正在加载账号信息...'
      emit('show-toast', '🎉 扫码登录成功！', 'success')
      
      const user = await GetUserInfo()
      if (user && user.isLogin) {
        emit('login-success', user)
        emit('close')
      }
    } else if (res.code === 86090) {
      qrStatusText.value = '扫码成功，请在手机上点击「确认登录」'
    } else if (res.code === 86038 || res.isExpired) {
      stopPolling()
      isQrExpired.value = true
      qrStatusText.value = '二维码已失效，请点击刷新'
    } else {
      qrStatusText.value = res.message || '等待手机 App 扫码...'
    }
  } catch (err) {
    // 轮询偶发网络错误忽略
  }
}

// 打开系统默认浏览器登录
async function handleOpenBrowser() {
  try {
    await OpenBrowserLogin()
    emit('show-toast', '已在默认浏览器中打开登录页面', 'info')
  } catch (err: any) {
    emit('show-toast', '打开浏览器失败: ' + err.message, 'error')
  }
}

// 同步浏览器登录状态 (支持自动读取 Chrome/Edge/Firefox 本地 Cookie)
async function handleSyncStatus() {
  isSyncingBrowser.value = true
  try {
    const user = await ExtractBrowserCookies()
    if (user && user.isLogin) {
      emit('show-toast', `从浏览器同步成功，欢迎回来 ${user.uname}！`, 'success')
      emit('login-success', user)
      emit('close')
    } else {
      // 降级检查内存态
      const fallbackUser = await GetUserInfo()
      if (fallbackUser && fallbackUser.isLogin) {
        emit('show-toast', `欢迎回来 ${fallbackUser.uname}！`, 'success')
        emit('login-success', fallbackUser)
        emit('close')
      } else {
        emit('show-toast', '未能在浏览器中检测到登录 Cookie（受系统沙盒保护）。建议使用「扫码登录」或在「填入 Cookie」中粘贴', 'info')
      }
    }
  } catch (err: any) {
    emit('show-toast', err.message || '未能自动同步浏览器 Cookie，推荐使用手机扫码登录', 'info')
  } finally {
    isSyncingBrowser.value = false
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
      emit('show-toast', `Cookie 验证成功，欢迎 ${user.uname}！`, 'success')
      emit('login-success', user)
      emit('close')
    } else {
      emit('show-toast', 'Cookie 已保存但无法获取用户信息，请检查 SESSDATA 是否正确', 'error')
    }
  } catch (err: any) {
    emit('show-toast', '保存失败: ' + err.message, 'error')
  } finally {
    isSavingCookie.value = false
  }
}

// 退出登录
async function handleLogout() {
  try {
    await Logout()
    emit('show-toast', '已退出当前账号', 'info')
    emit('logout-success')
    initQrLogin()
  } catch (err: any) {
    emit('show-toast', '退出失败: ' + err.message, 'error')
  }
}
</script>

<template>
  <div class="modal-overlay" @click.self="emit('close')">
    <div class="modal-content login-modal">
      <!-- Modal Header -->
      <div class="modal-header">
        <div class="header-title-box">
          <ShieldCheck class="header-icon" :size="20" />
          <h2 class="title-text">{{ userInfo?.isLogin ? '我的哔哩账号' : '哔哩哔哩账号登录' }}</h2>
        </div>
        <button class="btn-icon close-btn" @click="emit('close')" title="关闭">
          <X :size="18" />
        </button>
      </div>

      <!-- State A: Already Logged In Profile Card -->
      <div v-if="userInfo?.isLogin" class="profile-body">
        <div class="profile-top">
          <div class="avatar-large-box">
            <img
              :src="normalizeImg(userInfo.face)"
              class="avatar-large"
              referrerpolicy="no-referrer"
              alt="Avatar"
            />
            <div v-if="userInfo.vipStatus === 1" class="vip-badge-icon" title="大会员">
              <Crown :size="14" />
            </div>
          </div>

          <div class="profile-main-info">
            <div class="uname-row">
              <span class="uname-text">{{ userInfo.uname }}</span>
              <span class="level-tag">LV{{ userInfo.level }}</span>
              <span v-if="userInfo.vipLabel" class="vip-status-tag">
                {{ userInfo.vipLabel }}
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
            @click="activeTab = 'qr'"
          >
            <QrCode :size="15" />
            <span>扫码登录 (推荐)</span>
          </button>

          <button
            class="tab-btn"
            :class="{ active: activeTab === 'browser' }"
            @click="activeTab = 'browser'"
          >
            <Globe :size="15" />
            <span>浏览器同步</span>
          </button>

          <button
            class="tab-btn"
            :class="{ active: activeTab === 'cookie' }"
            @click="activeTab = 'cookie'"
          >
            <KeyRound :size="15" />
            <span>填入 Cookie</span>
          </button>
        </div>

        <!-- Tab 1: QR Code Scan Login (Most reliable & recommended) -->
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

        <!-- Tab 2: Browser Authorization -->
        <div v-show="activeTab === 'browser'" class="tab-pane browser-pane">
          <div class="browser-icon-box">
            <Globe :size="36" />
          </div>

          <h3 class="pane-title">在系统浏览器中登录并同步</h3>
          <p class="pane-desc">
            点击下方按钮前往 B 站登录，登录成功后点击「同步登录状态」
          </p>

          <div class="browser-actions">
            <button class="btn-primary browser-open-btn" @click="handleOpenBrowser">
              <ExternalLink :size="15" />
              <span>1. 打开浏览器登录 (优先 Chrome/Edge)</span>
            </button>

            <button
              class="btn-secondary browser-sync-btn"
              :disabled="isSyncingBrowser"
              @click="handleSyncStatus"
            >
              <RotateCw v-if="isSyncingBrowser" :size="14" class="spin-icon" />
              <CheckCircle2 v-else :size="14" />
              <span>2. {{ isSyncingBrowser ? '正在同步...' : '已在浏览器登录，同步状态' }}</span>
            </button>
          </div>

          <div class="mac-hint-box">
            <HelpCircle :size="13" class="hint-icon" />
            <span>说明: macOS 对 Safari 实施了系统沙盒隔离(TCC)。Chrome / Edge / Firefox 可一键自动提取；若使用 Safari，推荐使用左侧「扫码登录」1 秒搞定！</span>
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
            💡 提示: 在网页端登录 bilibili.com 后，按 F12 ➔ 应用程序 (Application) ➔ Cookies ➔ 复制 <code>SESSDATA</code> 对应的值粘贴即可。
          </div>

          <button
            class="btn-primary save-cookie-btn"
            :disabled="isSavingCookie || !manualCookie.trim()"
            @click="handleSaveCookie"
          >
            <KeyRound :size="14" />
            <span>{{ isSavingCookie ? '验证中...' : '保存并验证 Cookie' }}</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.login-modal {
  width: 440px;
  max-width: 90vw;
  background: var(--bg-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  overflow: hidden;
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border-subtle);
  background: rgba(255, 255, 255, 0.02);
}

.header-title-box {
  display: flex;
  align-items: center;
  gap: 8px;
}

.header-icon {
  color: var(--bili-pink);
}

.title-text {
  font-size: 15px;
  font-weight: 600;
  color: #fff;
}

/* Tab Switching */
.login-tabs {
  display: flex;
  border-bottom: 1px solid var(--border-subtle);
  background: rgba(0, 0, 0, 0.15);
  padding: 4px 6px;
  gap: 4px;
}

.tab-btn {
  flex: 1;
  height: 34px;
  background: transparent;
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 500;
  border-radius: var(--radius-sm);
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  transition: all var(--transition-fast);
}

.tab-btn:hover {
  color: var(--text-primary);
  background: rgba(255, 255, 255, 0.04);
}

.tab-btn.active {
  background: rgba(251, 114, 153, 0.12);
  color: var(--bili-pink);
  font-weight: 600;
}

.tab-pane {
  padding: 24px 20px;
  display: flex;
  flex-direction: column;
  align-items: center;
}

/* QR Code */
.qr-box {
  position: relative;
  width: 220px;
  height: 220px;
  border-radius: var(--radius-md);
  overflow: hidden;
  background: #ffffff;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4);
}

.qr-image {
  width: 100%;
  height: 100%;
}

.qr-loading-mask, .qr-expired-mask {
  position: absolute;
  inset: 0;
  background: rgba(0, 0, 0, 0.85);
  backdrop-filter: blur(4px);
  color: #fff;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  font-size: 13px;
}

.qr-expired-mask {
  cursor: pointer;
}

.refresh-hint {
  font-size: 11px;
  color: var(--bili-pink);
}

.qr-status-box {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 14px;
}

.status-device-icon {
  color: var(--bili-pink);
}

.qr-status-text {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
}

.qr-tips {
  margin-top: 10px;
  font-size: 11.5px;
  color: var(--text-muted);
  text-align: center;
  background: rgba(255, 255, 255, 0.03);
  padding: 8px 12px;
  border-radius: var(--radius-xs);
}

/* Browser Tab */
.browser-icon-box {
  width: 60px;
  height: 60px;
  border-radius: var(--radius-full);
  background: rgba(0, 174, 236, 0.12);
  color: var(--bili-blue);
  display: flex;
  align-items: center;
  justify-content: center;
  margin-bottom: 12px;
}

.pane-title {
  font-size: 15px;
  font-weight: 600;
  color: #fff;
  margin-bottom: 6px;
}

.pane-desc {
  font-size: 12px;
  color: var(--text-muted);
  text-align: center;
  max-width: 320px;
  margin-bottom: 20px;
}

.browser-actions {
  display: flex;
  flex-direction: column;
  gap: 10px;
  width: 100%;
}

.browser-open-btn, .browser-sync-btn {
  height: 38px;
  font-size: 13px;
  justify-content: center;
}

.mac-hint-box {
  margin-top: 14px;
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  color: var(--text-muted);
  background: rgba(255, 255, 255, 0.02);
  padding: 6px 10px;
  border-radius: var(--radius-xs);
}

.hint-icon {
  color: var(--bili-pink);
  flex-shrink: 0;
}

/* Cookie Tab */
.cookie-pane {
  align-items: stretch;
}

.item-label {
  font-size: 12px;
  color: var(--text-secondary);
  font-weight: 500;
  margin-bottom: 6px;
  display: block;
}

.cookie-textarea {
  width: 100%;
  height: 90px;
  padding: 8px 10px;
  font-size: 11.5px;
  font-family: monospace;
  resize: vertical;
}

.cookie-help {
  font-size: 11px;
  color: var(--text-muted);
  line-height: 1.5;
  margin: 10px 0 16px 0;
  background: rgba(255, 255, 255, 0.03);
  padding: 8px 10px;
  border-radius: var(--radius-xs);
}

.cookie-help code {
  color: var(--bili-pink);
  background: rgba(251, 114, 153, 0.1);
  padding: 1px 4px;
  border-radius: 3px;
}

.save-cookie-btn {
  height: 36px;
  font-size: 13px;
  justify-content: center;
}

/* Profile Body */
.profile-body {
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.profile-top {
  display: flex;
  align-items: center;
  gap: 14px;
}

.avatar-large-box {
  position: relative;
  width: 56px;
  height: 56px;
  flex-shrink: 0;
}

.avatar-large {
  width: 100%;
  height: 100%;
  border-radius: var(--radius-full);
  object-fit: cover;
  border: 2px solid rgba(255, 255, 255, 0.2);
}

.vip-badge-icon {
  position: absolute;
  bottom: -2px;
  right: -2px;
  background: var(--vip-gold);
  color: #000;
  border-radius: var(--radius-full);
  width: 18px;
  height: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.profile-main-info {
  display: flex;
  flex-direction: column;
  gap: 3px;
  overflow: hidden;
}

.uname-row {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.uname-text {
  font-size: 15px;
  font-weight: 700;
  color: #fff;
}

.level-tag {
  font-size: 10px;
  font-weight: 700;
  background: rgba(0, 174, 236, 0.15);
  color: var(--bili-blue);
  padding: 1px 5px;
  border-radius: 4px;
}

.vip-status-tag {
  font-size: 10px;
  font-weight: 700;
  background: rgba(251, 114, 153, 0.15);
  color: var(--bili-pink);
  padding: 1px 5px;
  border-radius: 4px;
}

.uid-text {
  font-size: 11px;
  color: var(--text-muted);
}

.vip-expire-text {
  font-size: 11px;
  color: var(--vip-gold);
}

.profile-stats-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}

.stat-card {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  background: rgba(255, 255, 255, 0.03);
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
  color: var(--vip-gold);
}

.stat-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
  overflow: hidden;
}

.stat-label {
  font-size: 10.5px;
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
  border-top: 1px solid var(--border-subtle);
  padding-top: 14px;
}

.logout-btn {
  height: 32px;
  padding: 0 12px;
  font-size: 12px;
  gap: 5px;
}

.spin-icon {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}
</style>
