<script setup lang="ts">
import { LocateFixed, Search, X } from 'lucide-vue-next'
import type { bilibili } from '../../wailsjs/go/models'

defineProps<{
  qualities: bilibili.QualityOption[]
  canLocate: boolean
}>()
const search = defineModel<string>('search', { required: true })
const quality = defineModel<string>('quality', { required: true })
const codec = defineModel<string>('codec', { required: true })
const emit = defineEmits<{ locate: [] }>()
</script>

<template>
  <div class="selector-toolbar">
    <div class="find-row">
      <div class="episode-search">
        <Search :size="15" aria-hidden="true" />
        <input v-model="search" type="search" placeholder="搜索标题、集号或范围，如 P8、1, 3-10" aria-label="搜索标题、集号或范围，例如 P8、1, 3-10" />
        <button v-if="search" class="clear-search" type="button" aria-label="清除搜索" @click="search = ''"><X :size="14" /></button>
      </div>
      <button v-if="canLocate" type="button" class="locate-button" title="定位当前链接" aria-label="定位当前链接" @click="emit('locate')">
        <LocateFixed :size="15" />
      </button>
    </div>
    <div class="format-row">
      <label>画质
        <select v-model="quality" class="quality-select" title="自动为每集选择最高可用画质">
          <option value="highest">自动</option>
          <option v-for="q in qualities" :key="q.id" :value="String(q.id)" :disabled="!q.isAvailable">
            {{ q.label }} {{ q.isVipRequired ? '(大会员)' : q.isLoginRequired ? '(需登录)' : '' }}
          </option>
        </select>
      </label>
      <label>编码
        <select v-model="codec">
          <option value="auto">自动</option>
          <option value="AVC">AVC / H.264</option>
          <option value="HEVC">HEVC / H.265</option>
          <option value="AV1">AV1</option>
        </select>
      </label>
    </div>
    <div class="selection-row"><slot /></div>
    <slot name="status" />
  </div>
</template>

<style scoped>
.selector-toolbar {
  flex-shrink: 0;
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 6px 16px;
  padding: 10px 20px;
  border-bottom: 1px solid var(--border-subtle);
}
.find-row, .format-row, .format-row label, .selection-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.find-row { min-width: 0; }
.selection-row { grid-column: 1 / -1; flex-wrap: wrap; }
.selector-toolbar :slotted(.toolbar-status) { grid-column: 1 / -1; margin: 0; }
.episode-search {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
  height: 30px;
  padding: 0 8px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-xs);
  background: var(--bg-card);
  color: var(--text-muted);
}
.episode-search:focus-within { border-color: var(--bili-pink); }
.episode-search input {
  flex: 1;
  min-width: 0;
  height: 100%;
  appearance: none;
  padding: 0;
  border: 0;
  background: transparent;
  box-shadow: none;
  outline: none;
  font-size: 12px;
}
.episode-search input::-webkit-search-cancel-button { display: none; }
.clear-search { display: flex; color: var(--text-muted); background: transparent; }
.locate-button {
  display: grid;
  place-items: center;
  flex: 0 0 30px;
  height: 30px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-xs);
  color: var(--text-secondary);
  background: var(--bg-card);
}
.locate-button:hover { background: var(--bg-card-hover); }
.format-row label { white-space: nowrap; color: var(--text-secondary); font-size: 11.5px; }
.format-row select { width: 120px; height: 30px; padding: 0 8px; font-size: 11.5px; }
.format-row .quality-select { width: 112px; }
@media (max-width: 600px) {
  .selector-toolbar { grid-template-columns: minmax(0, 1fr); }
}
</style>
