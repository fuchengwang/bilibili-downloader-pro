import { ref, watch } from 'vue'
import type { EnvironmentInfo } from '../wailsjs/runtime/runtime'

export type Theme = 'dark' | 'midnight' | 'light' | 'system'
export function normalizeTheme(value: unknown): Theme {
  return value === 'light' || value === 'midnight' || value === 'system' ? value : 'dark'
}

let cached: Theme = normalizeTheme(document.documentElement.dataset.startupTheme)
if (!document.documentElement.dataset.startupTheme) {
  try { cached = normalizeTheme(localStorage.getItem('bbdown-theme')) } catch {}
}
export const theme = ref<Theme>(cached)
const systemTheme = window.matchMedia('(prefers-color-scheme: dark)')

// Vite's live HTML does not carry the desktop's platform attribute.
if (!document.documentElement.dataset.platform) {
  void (window as any).runtime?.Environment?.().then(({ platform }: EnvironmentInfo) => {
    if (platform) document.documentElement.dataset.platform = platform
  }).catch(() => {})
}

function applyTheme() {
  const resolved = theme.value === 'system' ? (systemTheme.matches ? 'dark' : 'light') : theme.value
  document.documentElement.dataset.theme = resolved
  document.documentElement.style.colorScheme = resolved === 'light' ? 'light' : 'dark'
  try { localStorage.setItem('bbdown-theme', theme.value) } catch {}
  // Windows title bar follows the selected appearance; macOS uses the startup preference.
  const runtime = (window as any).runtime
  if (runtime) {
    // Keep the native backing colour in sync, including system-light startup.
    const background = resolved === 'light'
      ? [245, 245, 247]
      : resolved === 'midnight'
        ? [12, 14, 20]
        : [13, 13, 15]
    runtime.WindowSetBackgroundColour?.(...background, 255)
    if (resolved === 'light') runtime.WindowSetLightTheme?.()
    else runtime.WindowSetDarkTheme?.()
  }
}

watch(theme, applyTheme, { immediate: true, flush: 'sync' })
systemTheme.addEventListener('change', applyTheme)
