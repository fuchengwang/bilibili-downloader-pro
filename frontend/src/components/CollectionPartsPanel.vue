<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowLeft, CheckSquare, MinusSquare, Square, Search, X, ChevronLeft, ChevronRight } from 'lucide-vue-next'
import { bilibili } from '../../wailsjs/go/models'
import { filterEpisodes, selectEpisodes } from '../utils/episode-selection'

const props = defineProps<{
  video: bilibili.CollectionVideo
  selection: Record<number, boolean>
}>()
const emit = defineEmits<{
  (e: 'back'): void
  (e: 'select', selection: Record<number, boolean>): void
}>()
const query = ref('')
const onlySelected = ref(false)
const range = ref('')
const rangeError = ref('')
const page = ref(1)
const pageSize = 20
const chosen = computed(() => props.video.parts.filter(p => props.selection[p.cid]))
const matches = computed(() => filterEpisodes(props.video.parts.map(p => ({ ...p, index: p.page })), query.value))
const filtered = computed(() => onlySelected.value ? matches.value.filter(p => props.selection[p.cid]) : matches.value)
const filteredSelected = computed(() => filtered.value.filter(p => props.selection[p.cid]).length)
const allFilteredSelected = computed(() => filtered.value.length > 0 && filteredSelected.value === filtered.value.length)
const pageCount = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize)))
const visible = computed(() => filtered.value.slice((page.value - 1) * pageSize, page.value * pageSize))
watch([query, onlySelected, () => props.video.bvid], () => { page.value = 1 })
watch(pageCount, count => { if (page.value > count) page.value = count })
function toggle(cid: number) { emit('select', { ...props.selection, [cid]: !props.selection[cid] }) }
function selectResults(invert = false) { emit('select', selectEpisodes(props.selection, filtered.value, invert)) }
function toggleResults() {
  if (!allFilteredSelected.value) return selectResults()
  const next = { ...props.selection }
  for (const part of filtered.value) delete next[part.cid]
  emit('select', next)
}
function clearVideo() {
  const next = { ...props.selection }
  for (const part of props.video.parts) delete next[part.cid]
  emit('select', next)
}
function applyRange() {
  const raw = range.value.trim().replace(/，/g, ',')
  if (!raw) return
  if (!/^\d+(?:\s*-\s*\d+)?(?:\s*,\s*\d+(?:\s*-\s*\d+)?)*$/.test(raw)) {
    rangeError.value = '请输入分 P 序号，例如 1-10 或 1,3,5'
    return
  }
  const pages = new Set(props.video.parts.map(p => p.page))
  const requested = new Set<number>()
  for (const item of raw.split(',')) {
    const [start, end = start] = item.split('-').map(Number)
    if (start > end || end - start > props.video.parts.length || !pages.has(start) || !pages.has(end)) {
      rangeError.value = '范围超出当前视频的分 P 序号，请检查后重试'
      return
    }
    for (let i = start; i <= end; i++) if (pages.has(i)) requested.add(i)
  }
  emit('select', selectEpisodes(props.selection, props.video.parts.filter(p => requested.has(p.page))))
  range.value = ''; rangeError.value = ''
}
</script>

<template>
  <section class="parts-panel" aria-label="当前视频的分 P 选择">
    <div class="parts-heading">
      <button class="back-button" @click="emit('back')"><ArrowLeft :size="15" />合集</button>
      <h3>{{ video.title }}</h3>
      <p>第 {{ video.index }} 集 <span>·</span> {{ video.parts.length }} 个分 P <span>·</span> 已选 {{ chosen.length }} 个</p>
    </div>
    <div class="parts-tools">
      <label class="parts-search"><Search :size="15" /><input v-model="query" type="search" placeholder="搜索分 P 标题或序号，如 P8、1-10" aria-label="搜索此视频的分 P" /><button v-if="query" @click="query = ''" aria-label="清除分 P 搜索"><X :size="14" /></button></label>
      <div class="parts-actions">
        <button role="checkbox" :aria-checked="allFilteredSelected ? 'true' : filteredSelected ? 'mixed' : 'false'" :disabled="filtered.length === 0" @click="toggleResults"><CheckSquare v-if="allFilteredSelected" :size="14" /><MinusSquare v-else-if="filteredSelected" :size="14" /><Square v-else :size="14" />{{ allFilteredSelected ? '取消全选' : '全选' }}{{ query || onlySelected ? '结果' : '此视频' }}</button>
        <button :disabled="!filtered.length" @click="selectResults(true)">{{ query || onlySelected ? '反选结果' : '反选' }}</button>
        <button v-if="chosen.length || onlySelected" :class="{ active: onlySelected }" @click="onlySelected = !onlySelected">{{ onlySelected ? '显示全部' : '只看已选' }}</button>
        <button v-if="onlySelected && chosen.length" @click="clearVideo">清空此视频</button>
      </div>
    </div>
    <div class="parts-scroll">
      <button v-for="part in visible" :key="part.cid" class="part-option" :class="{ selected: selection[part.cid] }" role="checkbox" :aria-checked="!!selection[part.cid]" @click="toggle(part.cid)">
        <CheckSquare v-if="selection[part.cid]" :size="16" class="selected-icon" /><Square v-else :size="16" class="empty-icon" />
        <span class="part-number">P{{ part.page }}</span><span class="part-title">{{ part.title }}</span><span class="part-duration">{{ part.durationStr }}</span>
      </button>
      <div v-if="filtered.length === 0" class="parts-empty"><Search :size="24" /><p>{{ onlySelected ? '当前结果中没有已选分 P' : '没有找到匹配的分 P' }}</p></div>
    </div>
    <div v-if="pageCount > 1" class="parts-pagination"><span>{{ (page - 1) * pageSize + 1 }}–{{ Math.min(page * pageSize, filtered.length) }} / {{ filtered.length }} 个分 P</span><div><button :disabled="page === 1" @click="page--" aria-label="上一页分 P"><ChevronLeft :size="14" /></button><label>第 <select v-model="page" aria-label="分 P 页码"><option v-for="n in pageCount" :key="n" :value="n">{{ n }}</option></select> / {{ pageCount }} 页</label><button :disabled="page === pageCount" @click="page++" aria-label="下一页分 P"><ChevronRight :size="14" /></button></div></div>
  </section>
</template>

<style scoped>
.parts-panel { flex: 1; min-height: 0; min-width: 0; display: flex; flex-direction: column; overflow: hidden; }
.parts-heading { padding: 18px 24px 14px; flex-shrink: 0; }
.back-button { display: inline-flex; align-items: center; gap: 5px; padding: 0; border: 0; line-height: 1.5; color: var(--text-muted); font-size: 11px; background: transparent; margin-bottom: 10px; }
.back-button:hover { color: var(--bili-pink); }
h3 { color: var(--text-primary); font-size: 15px; font-weight: 600; line-height: 1.55; overflow-wrap: anywhere; margin: 0; max-height: 70px; overflow-y: auto; }
.parts-heading p { font-size: 11px; color: var(--text-muted); margin: 7px 0 0; }.parts-heading p span { margin: 0 6px; }
.parts-tools { padding: 0 24px 10px; display: flex; flex-direction: row; align-items: center; flex-wrap: wrap; gap: 8px 12px; border-bottom: 1px solid var(--border-subtle); flex-shrink: 0; }
.parts-search { display: flex; align-items: center; gap: 7px; flex: 1 1 210px; height: 32px; padding: 0 9px; background: var(--bg-input); border: 1px solid var(--border-subtle); border-radius: 7px; color: var(--text-muted); }.parts-search:focus-within { border-color: var(--bili-pink); }.parts-search input { flex: 1; min-width: 0; height: 100%; padding: 0; border: 0; background: transparent; box-shadow: none; outline: none; font-size: 11.5px; }.parts-search button { background: transparent; display: flex; color: var(--text-muted); }
.parts-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 4px; }.parts-actions button, .parts-range button { display: flex; align-items: center; gap: 4px; height: 28px; padding: 0 7px; border: 0; border-radius: 5px; background: transparent; color: var(--text-secondary); font-size: 11px; }.parts-actions button:first-child { color: var(--text-primary); font-weight: 500; }.parts-actions button:hover, .parts-range button:hover { background: var(--neutral-05); }.parts-actions .active { color: var(--bili-pink); }
.parts-range { width: 100%; display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: 9px 0 0; border-top: 1px solid var(--border-subtle); font-size: 11px; color: var(--text-muted); }.range-heading { width: 45px; flex-shrink: 0; }.range-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }.parts-range input { width: 160px; height: 29px; padding: 0 8px; font-size: 11.5px; }.parts-range button[type=submit] { color: var(--bili-pink); }.parts-range .range-error { width: 100%; color: var(--danger); }
.parts-scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 4px 24px 16px; display: flex; flex-direction: column; }
.part-option { flex-shrink: 0; display: flex; align-items: flex-start; gap: 12px; min-height: 43px; padding: 12px 8px; background: transparent; border: 0; border-bottom: 1px solid var(--border-subtle); border-radius: 0; text-align: left; color: var(--text-primary); }.part-option:hover { background: var(--neutral-03); }.part-option.selected { background: rgba(251,114,153,.06); }.part-option > svg { flex-shrink: 0; margin-top: 1px; }.selected-icon { color: var(--bili-pink); }.empty-icon { color: var(--text-muted); opacity: .75; }.part-number { flex-shrink: 0; min-width: 30px; color: var(--text-muted); font-size: 11px; font-variant-numeric: tabular-nums; line-height: 1.6; }.part-title { flex: 1; min-width: 0; overflow-wrap: anywhere; line-height: 1.5; font-size: 12.5px; }.part-duration { flex-shrink: 0; width: 55px; text-align: right; font-variant-numeric: tabular-nums; font-size: 11px; color: var(--text-muted); line-height: 1.7; }
.parts-pagination { flex-shrink: 0; padding: 8px 24px; min-height: 38px; border-top: 1px solid var(--border-subtle); display: flex; justify-content: space-between; align-items: center; color: var(--text-muted); font-size: 11px; }.parts-pagination > div { display: flex; align-items: center; gap: 8px; }.parts-pagination button { display: flex; padding: 5px; background: transparent; border-radius: 5px; color: var(--text-secondary); }.parts-pagination select { font-size: 11px; padding: 2px 5px; min-width: 42px; }.parts-empty { flex: 1; min-height: 100px; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px; color: var(--text-muted); font-size: 12px; }
button:disabled { opacity: .45; cursor: default; }button:focus-visible { outline: 2px solid var(--bili-pink); outline-offset: 2px; }
.parts-tools { gap: 12px 16px; padding-bottom: 12px; }
.parts-search { flex-basis: 100%; height: 34px; }
.parts-actions { flex: 1 1 300px; min-width: 0; gap: 4px; }
.parts-actions button { height: 30px; }
.parts-actions button:first-child { padding-left: 0; padding-right: 12px; margin-right: 4px; border-right: 1px solid var(--border-subtle); border-radius: 0; }
.sequence-select { flex: 0 0 190px; min-width: 190px; box-sizing: border-box; display: flex; align-items: center; height: 30px; border: 1px solid var(--border-subtle); border-radius: 6px; overflow: hidden; background: var(--bg-input); }
.sequence-select:focus-within { border-color: var(--bili-pink); }
.sequence-select input { flex: 1 1 0; width: 0; min-width: 0; height: 100%; border: 0; border-radius: 0; padding: 0 9px; background: transparent; font-size: 11px; outline: none; box-shadow: none; }
.sequence-select button { flex: 0 0 auto; appearance: none; box-shadow: none; border-radius: 0; height: 100%; border: 0; border-left: 1px solid var(--border-subtle); padding: 0 10px; background: var(--bg-card); color: var(--text-primary); font-size: 11px; }
.range-feedback { width: 100%; margin: 0; color: var(--danger); font-size: 11px; }
.parts-search, .sequence-select { height: 30px; box-sizing: border-box; background: var(--bg-card); }
.parts-search input, .parts-actions button, .sequence-select input, .sequence-select button { font-size: 11.5px; }
.parts-actions button:first-child { position: relative; padding: 0 7px; margin-right: 12px; border: 0; border-radius: 5px; }
.parts-actions button:first-child::after { content: ''; position: absolute; right: -8px; top: 7px; width: 1px; height: 16px; background: var(--border-subtle); pointer-events: none; }
.part-option { display: grid; grid-template-columns: 16px 30px minmax(0, 1fr) 55px; align-items: center; }
.part-option > svg { margin-top: 0; }
</style>
