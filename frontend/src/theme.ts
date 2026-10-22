import { ref, watch } from 'vue'

export type Theme = 'dark' | 'light' | 'system'
export function normalizeTheme(value: unknown): Theme {
  return value === 'light' || value === 'system' ? value : 'dark'
}

let cached: Theme = normalizeTheme(document.documentElement.dataset.startupTheme)
if (!document.documentElement.dataset.startupTheme) {
  try { cached = normalizeTheme(localStorage.getItem('bbdown-theme')) } catch {}
}
export const theme = ref<Theme>(cached)
const systemTheme = window.matchMedia('(prefers-color-scheme: dark)')

function applyTheme() {
  const resolved = theme.value === 'system' ? (systemTheme.matches ? 'dark' : 'light') : theme.value
  document.documentElement.dataset.theme = resolved
  document.documentElement.style.colorScheme = resolved
  try { localStorage.setItem('bbdown-theme', theme.value) } catch {}
  // Windows title bar follows the selected appearance; macOS uses the startup preference.
  const runtime = (window as any).runtime
  if (runtime) {
    // Keep the native backing colour in sync, including system-light startup.
    const background = resolved === 'light' ? [244, 246, 250] : [12, 14, 20]
    runtime.WindowSetBackgroundColour?.(...background, 255)
    if (resolved === 'light') runtime.WindowSetLightTheme?.()
    else runtime.WindowSetDarkTheme?.()
  }
}

watch(theme, applyTheme, { immediate: true, flush: 'sync' })
systemTheme.addEventListener('change', applyTheme)
