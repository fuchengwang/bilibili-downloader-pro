import { createApp } from 'vue'
import App from './App.vue'
import './styles/main.css'

document.addEventListener('contextmenu', (event) => {
  const target = event.target as HTMLElement | null
  if (target?.closest('input, textarea, [contenteditable="true"]')) return
  const selection = window.getSelection()?.toString().trim()
  if (selection) return
  event.preventDefault()
})

createApp(App).mount('#app')
