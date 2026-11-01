import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { ref, computed, watch, nextTick } from 'vue'

const utilSource = readFileSync(new URL('../src/utils/episode-selection.ts', import.meta.url), 'utf8')
const utilJS = ts.transpileModule(utilSource, { compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 } }).outputText
const { filterEpisodes, selectEpisodes } = await import(`data:text/javascript;base64,${Buffer.from(utilJS).toString('base64')}`)
function harness(component, props, extra = {}) {
  const source = readFileSync(new URL(`../src/components/${component}.vue`, import.meta.url), 'utf8').match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const ast = ts.createSourceFile('component.ts', source, ts.ScriptTarget.Latest, true)
  const script = ast.statements.filter(s => !ts.isImportDeclaration(s)).map(s => s.getText(ast)).join('\n')
  const code = ts.transpileModule(script, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None } }).outputText
  const messages = []
  const c = vm.createContext({ ref, computed, watch, nextTick, filterEpisodes, selectEpisodes, defineProps: () => props, defineEmits: () => (...args) => messages.push(args), onMounted: () => {}, onUnmounted: () => {}, ...extra })
  vm.runInContext(code, c)
  return { c, messages, run: code => vm.runInContext(code, c) }
}
const video = count => ({ bvid: 'BVfirst', index: 1, title: '第一册', parts: Array.from({ length: count }, (_, i) => ({ cid: i + 100, page: i + 1, index: 1, title: `练习${i + 1}` })) })

test('range and comma search filter by sequence without selecting', () => {
  const episodes = video(100).parts.map(p => ({ ...p, index: p.page }))
  assert.deepEqual(filterEpisodes(episodes, '1-3，95,100').map(p => p.index), [1, 2, 3, 95, 100])
  assert.deepEqual(filterEpisodes(episodes, 'P100').map(p => p.index), [100])
  assert.deepEqual(filterEpisodes(episodes, '8-2').map(p => p.index), [])
})

test('cancel all filtered parts preserves other selections across pages', () => {
  const chosen = Object.fromEntries(video(100).parts.map(p => [p.cid, true]))
  chosen[999] = true
  const { c, run, messages } = harness('CollectionPartsPanel', { video: video(100), selection: chosen })
  run("query.value = '81-100'")
  c.toggleResults()
  const next = messages.at(-1)[1]
  assert.equal(next[180], undefined)
  assert.equal(next[199], undefined)
  assert.equal(next[179], true)
  assert.equal(next[999], true)
})

test('two and three parts remain together; 100 parts are divided into bounded pages', () => {
  for (const n of [2, 3, 100]) {
    const { run } = harness('CollectionPartsPanel', { video: video(n), selection: {} })
    assert.equal(run('visible.value.length'), Math.min(n, 20))
    assert.equal(run('pageCount.value'), Math.ceil(n / 20))
    if (n === 100) { run('page.value = 5'); assert.equal(run('visible.value[0].page'), 81); assert.equal(run('visible.value.at(-1).page'), 100) }
  }
})

test('part search uses the page within the video and resets pagination', async () => {
  const { run } = harness('CollectionPartsPanel', { video: video(100), selection: {} })
  run("page.value = 5; query.value = 'P100'")
  await nextTick()
  assert.equal(run('page.value'), 1)
  assert.equal(run('visible.value.length'), 1)
  assert.equal(run('visible.value[0].page'), 100)
})

test('select all across 100 parts includes every page and preserves another video', () => {
  const { c, messages } = harness('CollectionPartsPanel', { video: video(100), selection: { 999: true } })
  c.selectResults()
  const selected = messages.at(-1)[1]
  assert.equal(Object.values(selected).filter(Boolean).length, 101)
  assert.equal(selected[199], true)
  assert.equal(selected[999], true)
})

test('range selection handles late parts and rejects reversed or out-of-bounds ranges', () => {
  const { c, run, messages } = harness('CollectionPartsPanel', { video: video(100), selection: { 999: true } })
  run("range.value = '95-100,2'; applyRange()")
  assert.equal(Object.values(messages.at(-1)[1]).filter(Boolean).length, 8)
  for (const range of ['5-2', '1-101', 'oops']) {
    c.badRange = range
    const before = messages.length
    run('range.value = badRange; applyRange()')
    assert.equal(messages.length, before)
    assert.ok(run('rangeError.value'))
  }
})

test('clearing one video leaves selections from other videos intact', () => {
  const { c, messages } = harness('CollectionPartsPanel', { video: video(3), selection: { 100: true, 101: true, 999: true } })
  c.clearVideo()
  assert.deepEqual(Object.keys(messages.at(-1)[1]), ['999'])
})

test('collection-level selection includes all nested parts in the submitted request', async () => {
  const first = video(100), second = { ...video(2), bvid: 'BVsecond', index: 2, parts: [{ cid: 900, page: 1, index: 2, title: 'A' }, { cid: 901, page: 2, index: 2, title: 'B' }] }
  const groups = { BVfirst: first, BVsecond: second }
  const detail = { type: 'normal', episodes: [{ bvid: 'BVfirst', index: 1, title: '第一册' }, { bvid: 'BVsecond', index: 2, title: '第二册' }] }
  const { c, run, messages } = harness('CollectionSelectorModal', { detail, initialQuality: 'highest', initialCodec: 'auto' }, { getCollectionVideoParts: async ep => groups[ep.bvid] })
  await c.loadGroups()
  c.selectAll()
  assert.equal(run('selectedList.value.length'), 102)
  c.handleSubmit()
  assert.equal(messages.at(-1)[1].length, 102)
  assert.deepEqual(Array.from(messages.at(-1)[4]), ['BVfirst', 'BVsecond'])
  c.toggleGroup(detail.episodes[0])
  assert.equal(run('selectedList.value.length'), 2)
})

test('retrying an incomplete collection preserves selections and reuses loaded groups', async () => {
  const first = video(2)
  const second = { ...video(2), bvid: 'BVsecond', parts: [{ cid: 900, page: 1 }, { cid: 901, page: 2 }] }
  const calls = { BVfirst: 0, BVsecond: 0 }
  const detail = { type: 'normal', episodes: [{ bvid: 'BVfirst' }, { bvid: 'BVsecond' }] }
  const { c, run } = harness('CollectionSelectorModal', { detail, initialQuality: 'highest', initialCodec: 'auto' }, {
    getCollectionVideoParts: async ep => {
      calls[ep.bvid]++
      if (ep.bvid === 'BVsecond' && calls.BVsecond === 1) throw new Error('temporary failure')
      return ep.bvid === 'BVfirst' ? first : second
    }
  })
  await c.loadGroups()
  c.toggleGroup(detail.episodes[0])
  await c.loadGroups()
  assert.equal(run('selectedList.value.length'), 2)
  assert.deepEqual(calls, { BVfirst: 1, BVsecond: 2 })
  assert.equal(run('loadError.value'), '')
})

test('a failed unselected video does not block a verified selection', async () => {
  const detail = { type: 'normal', episodes: [{ bvid: 'BVfirst', index: 1 }, { bvid: 'BVgone', index: 2 }] }
  const { c, run, messages } = harness('CollectionSelectorModal', { detail, initialQuality: 'highest', initialCodec: 'auto' }, {
    getCollectionVideoParts: async ep => {
      if (ep.bvid === 'BVgone') throw new Error('deleted video')
      return video(2)
    }
  })
  await c.loadGroups()
  c.toggleGroup(detail.episodes[0])
  assert.equal(run('canSubmit.value'), true)
  assert.equal(run('groupErrors.value.BVgone'), 'deleted video')
  c.handleSubmit()
  assert.deepEqual(Array.from(messages.at(-1)[4]), ['BVfirst'])
  c.toggleGroup(detail.episodes[1])
  assert.equal(run('selectedList.value.length'), 2)
})

test('loaded videos can be submitted while other videos are loading', async () => {
  let finish
  const pending = new Promise(resolve => { finish = resolve })
  const detail = { type: 'normal', episodes: [{ bvid: 'BVfirst', index: 1 }, { bvid: 'BVslow', index: 2 }] }
  const { c, run } = harness('CollectionSelectorModal', { detail, initialQuality: 'highest', initialCodec: 'auto' }, {
    getCollectionVideoParts: async ep => ep.bvid === 'BVfirst' ? video(2) : pending
  })
  const loading = c.loadGroups()
  await c.loadGroups() // A rapid second retry must reuse the running load.
  await new Promise(resolve => setImmediate(resolve))
  c.toggleGroup(detail.episodes[0])
  assert.equal(run('loading.value'), true)
  assert.equal(run('canSubmit.value'), true)
  assert.equal(run('selectionIncomplete.value'), true)
  finish({ ...video(2), bvid: 'BVslow' })
  await loading
  assert.equal(run('loadedCount.value'), 2)
})

test('an incomplete season allows verified entries and a pending submission cannot submit twice', async () => {
  const props = { detail: { type: 'normal', collectionIncomplete: true, episodes: [{ bvid: 'BVfirst', index: 1 }] }, initialQuality: 'highest', initialCodec: 'auto' }
  const { c, run, messages } = harness('CollectionSelectorModal', props, { getCollectionVideoParts: async () => video(2) })
  await c.loadGroups()
  c.selectAll()
  assert.equal(run('canSubmit.value'), true)
  props.submitting = true
  c.handleSubmit()
  assert.equal(messages.length, 0)
})
