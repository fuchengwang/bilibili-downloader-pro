<script setup lang="ts">
import { computed } from 'vue'
import {
  Sparkles,
  Download,
  CheckCircle2,
  Settings as SettingsIcon,
  User as UserIcon,
  Crown,
  Tv
} from 'lucide-vue-next'
import { bilibili } from '../../wailsjs/go/models'

const props = defineProps<{
  activeTab: string
  userInfo: bilibili.UserInfo | null
  activeTaskCount: number
  completedTaskCount?: number
  hasUpdate?: boolean
}>()

const emit = defineEmits<{
  (e: 'change-tab', tab: string): void
  (e: 'open-login'): void
}>()

const isVip = computed(() => {
  return props.userInfo?.isLogin && props.userInfo.vipStatus === 1
})

function normalizeImg(url?: string) {
  if (!url) return ''
  if (url.startsWith('//')) return 'https:' + url
  if (url.startsWith('http://')) return 'https://' + url.substring(7)
  return url
}
</script>

<template>
  <aside class="sidebar">
    <!-- App Logo & Brand (with top clearance for macOS traffic light buttons) -->
    <div class="brand" style="--wails-draggable: drag">
      <div class="logo-box">
        <img src="../assets/images/logo-universal.png" class="logo-img" alt="BBDown Logo" />
      </div>
      <div class="brand-text">
        <span class="brand-title">BBDown</span>
        <span class="brand-sub">PRO</span>
      </div>
    </div>

    <!-- Navigation Menu -->
    <nav class="nav-menu">
      <button
        class="nav-item"
        :class="{ active: activeTab === 'parse' }"
        @click="emit('change-tab', 'parse')"
      >
        <Sparkles class="nav-icon" :size="16" />
        <span class="nav-label">解析下载</span>
      </button>

      <button
        class="nav-item"
        :class="{ active: activeTab === 'queue' }"
        @click="emit('change-tab', 'queue')"
      >
        <Download class="nav-icon" :size="16" />
        <span class="nav-label">下载任务</span>
        <span v-if="activeTaskCount > 0" class="nav-badge">{{ activeTaskCount }}</span>
      </button>

      <button
        class="nav-item"
        :class="{ active: activeTab === 'history' }"
        @click="emit('change-tab', 'history')"
      >
        <CheckCircle2 class="nav-icon" :size="16" />
        <span class="nav-label">已完成</span>
        <span v-if="(completedTaskCount || 0) > 0" class="nav-badge completed-badge">{{ completedTaskCount }}</span>
      </button>

      <button
        class="nav-item"
        :class="{ active: activeTab === 'settings' }"
        @click="emit('change-tab', 'settings')"
      >
        <span class="settings-icon-wrap"><SettingsIcon class="nav-icon" :size="16" /><span v-if="hasUpdate" class="update-dot" title="有可用更新" aria-label="有可用更新"></span></span>
        <span class="nav-label">偏好设置</span>
      </button>
    </nav>

    <!-- Bottom User Account Card -->
    <div class="user-card" @click="emit('open-login')">
      <div class="avatar-wrap">
        <img
          v-if="userInfo?.isLogin && userInfo.face"
          :src="normalizeImg(userInfo.face)"
          class="user-avatar"
          referrerpolicy="no-referrer"
          alt="Avatar"
        />
        <div v-else class="avatar-placeholder">
          <UserIcon :size="16" />
        </div>
        <div v-if="isVip" class="vip-crown" title="大会员">
          <Crown :size="9" />
        </div>
      </div>

      <div class="user-info">
        <div class="user-name">
          {{ userInfo?.isLogin ? userInfo.uname : '点击登录账号' }}
        </div>
        <div class="user-status">
          <span v-if="userInfo?.isLogin" :class="isVip ? 'vip-text' : 'normal-text'">
            {{ userInfo.vipLabel || '普通用户' }}
          </span>
          <span v-else class="login-hint">登录解锁 4K/1080P60</span>
        </div>
      </div>
    </div>
  </aside>
</template>

<style scoped>
.sidebar {
  width: 200px;
  min-width: 200px;
  max-width: 200px;
  height: 100vh;
  background: var(--bg-sidebar);
  border-right: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  padding: 42px 12px 16px 12px; /* Top 42px creates elegant clearance under Mac traffic lights */
  box-sizing: border-box;
  flex-shrink: 0;
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 4px 6px 18px 6px;
  border-bottom: 1px solid var(--border-subtle);
  margin-bottom: 14px;
}

.logo-box {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.logo-img {
  width: 100%;
  height: 100%;
  object-fit: contain;
  filter: drop-shadow(0 4px 8px rgba(0, 0, 0, 0.3));
}

.brand-text {
  display: flex;
  align-items: center;
  gap: 6px;
}

.brand-title {
  font-size: 15px;
  font-weight: 700;
  color: var(--text-primary);
  letter-spacing: -0.2px;
}

.brand-sub {
  font-size: 9px;
  font-weight: 700;
  padding: 1px 4px;
  border-radius: 4px;
  background: rgba(251, 114, 153, 0.15);
  color: var(--bili-pink);
  border: 1px solid rgba(251, 114, 153, 0.25);
  letter-spacing: 0.5px;
}

.nav-menu {
  display: flex;
  flex-direction: column;
  gap: 4px;
  flex: 1;
}

.nav-item {
  width: 100%;
  height: 38px;
  padding: 0 12px;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 13px;
  font-weight: 500;
  transition: background var(--transition-fast), color var(--transition-fast);
  border: none;
  text-align: left;
}

.nav-item:hover {
  background: var(--neutral-05);
  color: var(--text-primary);
}

.nav-item.active {
  background: rgba(251, 114, 153, 0.12);
  color: var(--bili-pink);
  font-weight: 600;
}

.settings-icon-wrap { position: relative; display: inline-flex; flex-shrink: 0; }
.update-dot { position: absolute; top: -3px; right: -3px; width: 6px; height: 6px; border-radius: 50%; background: #00aeec; box-shadow: 0 0 0 2px var(--bg-sidebar); }

.nav-icon {
  flex-shrink: 0;
  color: inherit;
}

.nav-label {
  flex: 1;
  white-space: nowrap;
}

.nav-badge {
  background: var(--bili-pink);
  color: #ffffff;
  font-size: 10px;
  font-weight: 700;
  padding: 1px 6px;
  border-radius: var(--radius-full);
  line-height: 1.3;
}

.nav-badge.completed-badge {
  background: rgba(16, 185, 129, 0.25);
  color: #34d399;
  border: 1px solid rgba(16, 185, 129, 0.4);
}

.user-card {
  margin-top: auto;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: var(--radius-sm);
  background: var(--neutral-03);
  border: 1px solid var(--border-subtle);
  cursor: pointer;
  transition: all var(--transition-fast);
}

.user-card:hover {
  background: var(--neutral-07);
  border-color: rgba(251, 114, 153, 0.3);
}

.avatar-wrap {
  position: relative;
  width: 32px;
  height: 32px;
  flex-shrink: 0;
}

.user-avatar {
  width: 100%;
  height: 100%;
  border-radius: var(--radius-full);
  object-fit: cover;
  border: 1px solid var(--neutral-15);
}

.avatar-placeholder {
  width: 100%;
  height: 100%;
  border-radius: var(--radius-full);
  background: var(--neutral-08);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-secondary);
}

.vip-crown {
  position: absolute;
  bottom: -2px;
  right: -2px;
  background: var(--vip-gold);
  color: #000000;
  border-radius: var(--radius-full);
  width: 13px;
  height: 13px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.user-info {
  display: flex;
  flex-direction: column;
  overflow: hidden;
  gap: 1px;
}

.user-name {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.user-status {
  font-size: 10.5px;
  line-height: 1.2;
}

.vip-text {
  color: var(--vip-gold);
  font-weight: 600;
}

.normal-text {
  color: var(--text-muted);
}

.login-hint {
  color: var(--bili-pink);
}
</style>
