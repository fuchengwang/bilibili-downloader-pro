<script setup lang="ts">
import { ref, computed, nextTick, onMounted, onUnmounted } from 'vue'
import { ChevronDown, Check } from 'lucide-vue-next'

defineOptions({
  inheritAttrs: false
})

export interface SelectOption {
  value: string | number
  label: string
  badge?: string
  disabled?: boolean
}

const props = withDefaults(defineProps<{
  modelValue: string | number
  options: SelectOption[]
  disabled?: boolean
  size?: 'sm' | 'md'
  placeholder?: string
  id?: string
  ariaLabel?: string
}>(), {
  disabled: false,
  size: 'md',
  placeholder: '请选择'
})

const emit = defineEmits<{
  (e: 'update:modelValue', value: string | number): void
  (e: 'change', value: string | number): void
}>()

const triggerRef = ref<HTMLButtonElement | null>(null)
const menuRef = ref<HTMLDivElement | null>(null)
const isOpen = ref(false)
const activeIndex = ref(-1)
const menuStyle = ref<Record<string, string>>({})

const selectedOption = computed(() =>
  props.options.find(opt => opt.value === props.modelValue)
)

const displayLabel = computed(() =>
  selectedOption.value?.label || props.placeholder
)

function updateMenuPosition() {
  const trigger = triggerRef.value
  if (!trigger) return
  const rect = trigger.getBoundingClientRect()
  const menuHeight = Math.min(props.options.length * 36 + 12, 260)
  const spaceBelow = window.innerHeight - rect.bottom - 12
  const spaceAbove = rect.top - 12
  const openUpward = spaceBelow < menuHeight && spaceAbove > spaceBelow
  const width = Math.max(rect.width, 132)
  const left = Math.min(Math.max(8, rect.left), window.innerWidth - width - 8)
  const maxHeight = Math.max(120, Math.min(260, openUpward ? spaceAbove : spaceBelow))

  menuStyle.value = {
    position: 'fixed',
    left: `${left}px`,
    minWidth: `${rect.width}px`,
    maxWidth: `${Math.max(rect.width + 80, 280)}px`,
    maxHeight: `${maxHeight}px`,
    ...(openUpward
      ? { bottom: `${window.innerHeight - rect.top + 6}px`, top: 'auto' }
      : { top: `${rect.bottom + 6}px`, bottom: 'auto' })
  }
}

async function openMenu() {
  if (props.disabled || isOpen.value) return
  isOpen.value = true
  const currentIdx = props.options.findIndex(o => o.value === props.modelValue && !o.disabled)
  activeIndex.value = currentIdx >= 0 ? currentIdx : props.options.findIndex(o => !o.disabled)
  await nextTick()
  updateMenuPosition()
}

function closeMenu() {
  isOpen.value = false
  activeIndex.value = -1
}

function toggleMenu() {
  if (isOpen.value) closeMenu()
  else void openMenu()
}

function selectOption(option: SelectOption) {
  if (option.disabled) return
  if (option.value !== props.modelValue) {
    emit('update:modelValue', option.value)
    emit('change', option.value)
  }
  closeMenu()
  triggerRef.value?.focus()
}

function moveActive(step: 1 | -1) {
  const count = props.options.length
  if (count === 0) return
  let idx = activeIndex.value
  for (let i = 0; i < count; i++) {
    idx = (idx + step + count) % count
    if (!props.options[idx].disabled) {
      activeIndex.value = idx
      return
    }
  }
}

function handleKeydown(event: KeyboardEvent) {
  if (props.disabled) return
  if (event.key === 'Escape') {
    if (isOpen.value) {
      event.preventDefault()
      closeMenu()
    }
    return
  }
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    if (!isOpen.value) void openMenu()
    else moveActive(1)
    return
  }
  if (event.key === 'ArrowUp') {
    event.preventDefault()
    if (!isOpen.value) void openMenu()
    else moveActive(-1)
    return
  }
  if (event.key === 'Enter' || event.key === ' ') {
    if (isOpen.value && activeIndex.value >= 0) {
      event.preventDefault()
      selectOption(props.options[activeIndex.value])
    } else if (!isOpen.value) {
      event.preventDefault()
      void openMenu()
    }
  }
}

function handleDocumentPointerDown(event: PointerEvent) {
  if (!isOpen.value) return
  const target = event.target as Node | null
  if (!target) return
  if (triggerRef.value?.contains(target) || menuRef.value?.contains(target)) return
  closeMenu()
}

function handleWindowScroll(event: Event) {
  if (!isOpen.value) return
  if (event.target instanceof Node && menuRef.value?.contains(event.target)) return
  closeMenu()
}

function handleWindowResize() {
  if (isOpen.value) closeMenu()
}

onMounted(() => {
  document.addEventListener('pointerdown', handleDocumentPointerDown)
  window.addEventListener('scroll', handleWindowScroll, true)
  window.addEventListener('resize', handleWindowResize)
})

onUnmounted(() => {
  document.removeEventListener('pointerdown', handleDocumentPointerDown)
  window.removeEventListener('scroll', handleWindowScroll, true)
  window.removeEventListener('resize', handleWindowResize)
})
</script>

<template>
  <button
    v-bind="$attrs"
    :id="id"
    ref="triggerRef"
    type="button"
    class="app-select-trigger"
    :class="[`size-${size}`, { open: isOpen, disabled }]"
    :disabled="disabled"
    :aria-label="ariaLabel"
    :aria-expanded="isOpen"
    aria-haspopup="listbox"
    @click="toggleMenu"
    @keydown="handleKeydown"
  >
    <span class="trigger-text">{{ displayLabel }}</span>
    <span v-if="selectedOption?.badge" class="trigger-badge">{{ selectedOption.badge }}</span>
    <ChevronDown class="trigger-chevron" :size="14" aria-hidden="true" />
  </button>

  <Teleport to="body">
    <Transition name="select-popover">
      <div
        v-if="isOpen"
        ref="menuRef"
        class="app-select-menu"
        role="listbox"
        :style="menuStyle"
        @keydown="handleKeydown"
      >
        <button
          v-for="(option, index) in options"
          :key="String(option.value)"
          type="button"
          role="option"
          class="app-select-option"
          :class="{
            selected: option.value === modelValue,
            highlighted: index === activeIndex,
            disabled: option.disabled
          }"
          :aria-selected="option.value === modelValue"
          :disabled="option.disabled"
          @mouseenter="activeIndex = index"
          @click="selectOption(option)"
        >
          <span class="option-label">{{ option.label }}</span>
          <span v-if="option.badge" class="option-badge">{{ option.badge }}</span>
          <Check v-if="option.value === modelValue" class="option-check" :size="14" :stroke-width="2.5" aria-hidden="true" />
        </button>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.app-select-trigger {
  width: 100%;
  min-width: 0;
  display: inline-flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 10px 0 11px;
  background: var(--bg-control);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  box-shadow: var(--surface-highlight);
  color: var(--text-primary);
  font-size: 12.5px;
  font-weight: 500;
  text-align: left;
  transition: background var(--transition-fast), border-color var(--transition-fast), box-shadow var(--transition-fast);
}

.app-select-trigger.size-md {
  height: 34px;
}

.app-select-trigger.size-sm {
  height: 30px;
  font-size: 12px;
  border-radius: var(--radius-xs);
}

.app-select-trigger:hover:not(:disabled) {
  background: var(--bg-control-hover);
  border-color: var(--neutral-12);
}

.app-select-trigger.open,
.app-select-trigger:focus-visible {
  border-color: var(--border-focus);
  box-shadow: 0 0 0 2px var(--bili-pink-glow);
}

.app-select-trigger:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.trigger-text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.trigger-badge {
  font-size: 10px;
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--neutral-08);
  color: var(--text-muted);
  flex-shrink: 0;
}

.trigger-chevron {
  color: var(--text-muted);
  flex-shrink: 0;
  transition: transform var(--transition-fast), color var(--transition-fast);
}

.app-select-trigger.open .trigger-chevron {
  transform: rotate(180deg);
  color: var(--bili-pink);
}
</style>

<style>
.app-select-menu {
  z-index: 2000;
  overflow-y: auto;
  background: var(--bg-popover);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border: 1px solid var(--border-popover);
  border-radius: 12px;
  box-shadow: var(--shadow-popover);
  padding: 5px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.app-select-option {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 7px 10px;
  border: none;
  border-radius: 8px;
  background: transparent;
  color: var(--text-secondary);
  font-size: 12.5px;
  font-weight: 500;
  text-align: left;
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease;
}

.app-select-option:active {
  transform: none;
}

.app-select-option.highlighted:not(.disabled),
.app-select-option:hover:not(.disabled) {
  background: var(--neutral-08);
  color: var(--text-primary);
}

.app-select-option.selected {
  background: rgba(244, 102, 143, 0.12);
  color: var(--bili-pink);
  font-weight: 600;
}

.app-select-option.disabled {
  opacity: 0.42;
  cursor: not-allowed;
}

.option-label {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.option-badge {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--neutral-06);
  color: var(--text-muted);
  flex-shrink: 0;
}

.option-check {
  color: var(--bili-pink);
  flex-shrink: 0;
}

.select-popover-enter-active,
.select-popover-leave-active {
  transition: opacity 0.14s ease, transform 0.14s cubic-bezier(0.16, 1, 0.3, 1);
}

.select-popover-enter-from,
.select-popover-leave-to {
  opacity: 0;
  transform: translateY(-4px) scale(0.98);
}
</style>
