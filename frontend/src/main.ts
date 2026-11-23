import { createApp } from 'vue'
import App from './App.vue'
import './styles/main.css'
import { Environment } from '../wailsjs/runtime/runtime'

// Packaged builds receive the platform with the initial HTML.
if (!document.documentElement.dataset.platform && (window as any).runtime?.Environment) {
  void Environment().then((env) => {
    if (env?.platform) {
      document.documentElement.dataset.platform = env.platform
    }
  })
  .catch(() => {})
}

document.addEventListener('contextmenu', (event) => {
  const target = event.target as HTMLElement | null
  if (target?.closest('input, textarea, [contenteditable="true"]')) return
  const selection = window.getSelection()?.toString().trim()
  if (selection) return
  event.preventDefault()
})

createApp(App).mount('#app')
