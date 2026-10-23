import type { bilibili } from '../wailsjs/go/models'

// The desktop supplies its local profile with the initial HTML. Reading it
// synchronously avoids waiting for WebView IPC before displaying the account.
export function readStartupAccount(): bilibili.UserInfo | null {
  try {
    const source = document.getElementById('bbdown-startup-account')?.textContent
    const user = source ? JSON.parse(source) : null
    if (user && typeof user.isLogin === 'boolean') return user
  } catch {}
  return null
}
