import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { ref, computed, watch, nextTick, shallowReactive, reactive, effectScope } from 'vue'

const utilSource = readFileSync(new URL('../src/utils/episode-selection.ts', import.meta.url), 'utf8')
const utilJS = ts.transpileModule(utilSource, { compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 } }).outputText
const { filterEpisodes, nearEpisode, selectEpisodes } = await import(`data:text/javascript;base64,${Buffer.from(utilJS).toString('base64')}`)
const knownAccess = {
  referenceQualities: ref([]),
  accessFor: () => ({ state: 'available' }),
  canSelect: () => true,
}
function harness(component, props, extra = {}) {
  const source = readFileSync(new URL(`../src/components/${component}.vue`, import.meta.url), 'utf8').match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const ast = ts.createSourceFile('component.ts', source, ts.ScriptTarget.Latest, true)
  const script = ast.statements.filter(s => !ts.isImportDeclaration(s)).map(s => s.getText(ast)).join('\n')
  const code = ts.transpileModule(script, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None } }).outputText
  const messages = []
  const c = vm.createContext({
    ref, computed, watch, nextTick, filterEpisodes, nearEpisode, selectEpisodes,
    useEpisodePlayback: () => knownAccess,
    getCachedCollectionVideoParts: () => undefined,
    useDownloadedEpisodes: () => () => false,
    expectedEpisodeQualities: () => undefined,
    defineProps: () => component === 'CollectionPartsPanel' ? { ...knownAccess, ...props } : props,
    defineEmits: () => (...args) => messages.push(args),
    onMounted: () => {}, onUnmounted: () => {}, ...extra,
  })
  vm.runInContext(code, c)
  return { c, messages, run: code => vm.runInContext(code, c) }
}
const video = count => ({ bvid: 'BVfirst', index: 1, title: '第一册', parts: Array.from({ length: count }, (_, i) => ({ cid: i + 100, page: i + 1, index: 1, title: `练习${i + 1}` })) })

test('a checked reference part and deferred parts keep the collection checkbox selectable', () => {
  const group = video(8)
  const episode = { bvid: group.bvid, cid: 100, index: 34 }
  const { c, run } = harness('CollectionSelectorModal', {
    detail: { type: 'normal', episodes: [episode], collectionVideos: { [group.bvid]: group } },
    initialQuality: 'highest', initialCodec: 'auto',
  }, {
    useEpisodePlayback: () => ({
      ...knownAccess,
      accessFor: cid => ({ state: cid === 100 ? 'available' : 'unchecked' }),
    }),
  })
  assert.equal(run('groupAccessByVideo.value.BVfirst.state'), 'unchecked')
  c.toggleGroup(episode)
  assert.equal(run('selectedList.value.length'), 8)
})

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

test('range selection waits for playback checks without losing the input or existing picks', () => {
  let checking = true
  const { run, messages } = harness('CollectionPartsPanel', {
    video: video(3),
    selection: { 999: true },
    canSelect: cid => cid === 100 || !checking,
    accessFor: () => ({ state: checking ? 'checking' : 'available' }),
  })
  run("range.value = '1-3'; applyRange()")
  assert.equal(messages.length, 0)
  assert.equal(run('range.value'), '1-3')
  assert.match(run('rangeError.value'), /P2.*正在检查/)

  checking = false
  run('applyRange()')
  assert.deepEqual(Object.keys(messages.at(-1)[1]), ['100', '101', '102', '999'])
  assert.equal(run('range.value'), '')
  assert.equal(run('rangeError.value'), '')
})

test('range selection explains a blocked or missing part and avoids partial selection', () => {
  const blocked = harness('CollectionPartsPanel', {
    video: video(3),
    selection: {},
    canSelect: cid => cid !== 101,
    accessFor: () => ({ state: 'blocked', reason: '需要大会员' }),
  })
  blocked.run("range.value = '1-3'; applyRange()")
  assert.equal(blocked.messages.length, 0)
  assert.match(blocked.run('rangeError.value'), /P2.*需要大会员/)

  const sparse = video(4)
  sparse.parts.splice(1, 1)
  const missing = harness('CollectionPartsPanel', { video: sparse, selection: {} })
  missing.run("range.value = '1-3'; applyRange()")
  assert.equal(missing.messages.length, 0)
  assert.match(missing.run('rangeError.value'), /缺少 P2/)
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

test('reopening a collection renders cached parts before fetching any new groups', async () => {
  const group = video(2)
  const detail = { type: 'normal', episodes: [{ bvid: group.bvid, index: 1, cid: 100 }] }
  let fetches = 0
  const { c, run } = harness('CollectionSelectorModal', { detail, initialQuality: '16', initialCodec: 'auto' }, {
    getCachedCollectionVideoParts: () => group,
    getCollectionVideoParts: async () => { fetches++; return group },
  })
  assert.equal(run('visibleParts.value.length'), 2)
  await c.loadGroups()
  assert.equal(fetches, 0)
})

test('314 embedded ordinary videos can be selected while the reference and paid video are checked', async () => {
  const source = readFileSync(new URL('../src/utils/episode-playback.ts', import.meta.url), 'utf8')
  const requests = []
  let dispose
  const imports = {
    vue: { computed, watch, shallowReactive, onScopeDispose: callback => { dispose = callback } },
    '../../wailsjs/go/main/App': {
      GetPlaybackInfo: (_bvid, _aid, cid) => new Promise(resolve => requests.push({ cid, resolve })),
    },
    './episode-selection': { isPermissionError: kind => kind === 'charge_required', nearEpisode },
  }
  const exports = {}
  vm.runInNewContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText,
    { exports, require: name => imports[name] })
  const episodes = Array.from({ length: 315 }, (_, i) => ({
    bvid: `BV${i + 1}`, index: i + 1, cid: i + 1, requiresAccessCheck: i === 314,
  }))
  const detail = {
    type: 'normal', hasLinkedEpisode: true, episodes, collectionId: 12,
    collectionVideos: Object.fromEntries(episodes.map(ep => [ep.bvid, { bvid: ep.bvid, parts: [ep] }])),
  }
  const { c, run, messages } = harness('CollectionSelectorModal', { detail, initialQuality: 'highest', initialCodec: 'auto' }, {
    ...exports,
    getCollectionVideoParts: () => assert.fail('embedded metadata was fetched again'),
  })
  try {
    assert.deepEqual(requests.map(request => request.cid), [1, 315])
    assert.equal(run('visibleParts.value.length'), 314)
    c.selectAll()
    c.handleSubmit()
    assert.equal(messages.at(-1)[1].length, 314)
    assert.equal(messages.at(-1)[1].includes(315), false)
    await c.loadGroups()
    assert.equal(requests.length, 2)
  } finally {
    dispose()
    for (const request of requests) request.resolve({})
  }
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
  assert.equal(run('Object.keys(groups.value).length'), 1)
  finish({ ...video(2), bvid: 'BVslow' })
  await loading
  assert.equal(run('Object.keys(groups.value).length'), 2)
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

test('batch selection uses per-episode playback results and ignores responses for the previous account', async () => {
  const source = readFileSync(new URL('../src/utils/episode-playback.ts', import.meta.url), 'utf8')
  const imports = {
    vue: { computed, watch, shallowReactive, onScopeDispose: callback => { dispose = callback } },
    '../../wailsjs/go/main/App': { GetPlaybackInfo: (_, __, cid, epid) => new Promise(resolve => requests.push({ cid, epid, resolve })) },
    './episode-selection': { isPermissionError: kind => kind === 'preview_only', nearEpisode },
  }
  const requests = []
  let dispose
  const exports = {}
  vm.runInNewContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText,
    { exports, require: name => imports[name] })
  const props = reactive({
    detail: {
      type: 'bangumi', defaultPage: 2, hasLinkedEpisode: true,
      episodes: [1, 2, 3, 4].map(cid => ({ cid, epid: cid + 10, index: cid })),
    },
    userInfo: { isLogin: true, mid: 1 },
  })
  const { c, run, messages } = harness('EpisodeSelectorModal', props, exports)
  try {
    assert.deepEqual(requests.map(r => r.epid), [12, 13, 11])
    c.selectAll()
    assert.equal(run('selectedList.value.length'), 0)

    props.userInfo = { isLogin: true, mid: 2 }
    for (const r of requests.slice()) r.resolve({ error: { kind: 'preview_only' } })
    await new Promise(resolve => setImmediate(resolve))
    assert.equal(run('accessFor(1).state'), 'checking')
    assert.deepEqual(requests.slice(3).map(r => r.cid), [2, 3, 1])

    requests[3].resolve({ error: { kind: 'preview_only', message: '只能试看' } })
    requests[4].resolve({ qualities: [{ isAvailable: false }] })
    requests[5].resolve({ qualities: [{ isAvailable: true }] })
    await new Promise(resolve => setImmediate(resolve))
    assert.equal(requests[6].cid, 4)
    assert.equal(run('accessFor(2).state'), 'blocked')
    assert.equal(run('accessFor(3).state'), 'unknown')

    c.selectAll()
    c.handleSubmit()
    assert.deepEqual(Array.from(messages.at(-1)[1]), [1, 3])
    c.deselectAll()
    c.toggleEpisode(2)
    run("searchQuery.value = '1-4'; selectAll()")
    c.invertSelection()
    assert.equal(run('selectedList.value.length'), 0)

    requests[6].resolve({ qualities: [{ id: 16, isAvailable: true }] })
    await new Promise(resolve => setImmediate(resolve))
    dispose()
    const reopened = harness('EpisodeSelectorModal', props, exports)
    assert.equal(reopened.run('accessFor(2).state'), 'blocked')
    assert.equal(reopened.run('accessFor(4).state'), 'available')
    assert.equal(requests.length, 7)
  } finally {
    dispose()
    requests[6]?.resolve({})
  }
})

test('downloaded requires an existing file and the actual selected quality and codec', async () => {
  const source = readFileSync(new URL('../src/utils/downloaded-episodes.ts', import.meta.url), 'utf8')
  const exports = {}
  let checked
  const fileCheck = new Promise(resolve => { checked = resolve })
  vm.runInNewContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, {
    exports, setTimeout, clearTimeout,
    require: name => name === 'vue' ? { ref, computed, watch } : {
      CheckHistoryFiles: async paths => {
        checked(Array.from(paths))
        return { '/present.mp4': 'present', '/missing.mp4': 'missing' }
      },
    },
  })
  const quality = ref('16'), codec = ref('auto')
  const episodes = [{ cid: 101 }, { cid: 102 }]
  const history = [
    { cid: 101, status: 'completed', qualityId: 16, targetQuality: '80', codec: 'AVC', outputPath: '/present.mp4' },
    { cid: 102, status: 'completed', qualityId: 16, codec: 'AVC', outputPath: '/missing.mp4' },
  ]
  const scope = effectScope()
  try {
    const downloaded = scope.run(() => exports.useDownloadedEpisodes(() => episodes, () => history,
      () => 'normal', () => quality.value, () => codec.value, () => [
        { id: 80, isAvailable: true, codecs: 'HEVC' }, { id: 16, isAvailable: true, codecs: 'AVC' },
      ]))
    assert.deepEqual(await fileCheck, ['/present.mp4', '/missing.mp4'])
    await new Promise(resolve => setImmediate(resolve))
    assert.equal(downloaded(101), true)
    assert.equal(downloaded(102), false)
    quality.value = '80'
    assert.equal(downloaded(101), false)
    quality.value = 'highest'
    assert.equal(downloaded(101), false)
    codec.value = 'AVC'
    assert.equal(downloaded(101), true)
    quality.value = '16'
    codec.value = 'HEVC'
    assert.equal(downloaded(101), false)
    codec.value = 'AVC'
    assert.equal(downloaded(101), true)
  } finally {
    scope.stop()
  }
})

test('submitting 314 episodes closes the selector before the Go call settles and keeps failures retryable', async () => {
  const app = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')
  const source = app.slice(app.indexOf('function handleEpisodeBatchSubmit'), app.indexOf('function showDownloadResults'))
  const episodes = Array.from({ length: 314 }, (_, i) => ({ cid: i + 1 }))
  const requests = []
  const c = vm.createContext({
    activeEpisodeDetail: ref({ bvid: 'BVroot', episodes, type: 'normal' }),
    activeTab: ref('parse'), showEpisodeModal: ref(true),
    batchSubmitting: ref(false), batchRequest: ref(null), batchError: ref(''),
    AddDownloadTasks: req => new Promise((resolve, reject) => requests.push({ req, resolve, reject })),
    showDownloadResults: () => {}, showToast: () => {},
  })
  vm.runInContext(ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, c)
  c.handleEpisodeBatchSubmit(episodes.map(ep => ep.cid), '16', 'auto')
  assert.equal(c.showEpisodeModal.value, false)
  assert.equal(c.activeTab.value, 'queue')
  assert.equal(c.batchSubmitting.value, true)
  assert.equal(requests[0].req.episodes.length, 314)
  requests[0].reject(new Error('模拟写入失败'))
  await new Promise(resolve => setImmediate(resolve))
  assert.equal(c.batchError.value, '模拟写入失败')
  const retry = c.enqueueEpisodeBatch(c.batchRequest.value)
  assert.deepEqual(requests[1].req.episodes, requests[0].req.episodes)
  requests[1].resolve([])
  await retry
  assert.equal(c.batchRequest.value, null)
})

test('both selector types restore the format after dismissal and reset it when a different link opens', () => {
  const app = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')
  const source = app.slice(app.indexOf('function handleOpenEpisodes'), app.indexOf('// 单集快速下载'))
  for (const component of ['EpisodeSelectorModal', 'CollectionSelectorModal']) {
    const detail = { type: 'normal', episodes: [] }
    const c = vm.createContext({
      activeEpisodeDetail: ref(null), activeEpisodeLinkVersion: ref(null), activeEpisodeQuality: ref('highest'),
      activeEpisodeCodec: ref('auto'), showEpisodeModal: ref(false),
    })
    vm.runInContext(ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, c)
    const open = version => {
      c.handleOpenEpisodes(detail, '64', 'AV1', version)
      return harness(component, {
        detail, initialQuality: c.activeEpisodeQuality.value, initialCodec: c.activeEpisodeCodec.value,
      })
    }

    const first = open(1)
    first.c.updateFormat('16', 'HEVC')
    const [event, quality, codec] = first.messages.at(-1)
    assert.equal(event, 'format-change')
    c.handleEpisodeFormatChange(quality, codec)
    c.showEpisodeModal.value = false

    const reopened = open(1)
    assert.equal(reopened.run('targetQuality.value'), '16')
    assert.equal(reopened.run('targetCodec.value'), 'HEVC')
    const nextLink = open(2)
    assert.equal(nextLink.run('targetQuality.value'), '64')
    assert.equal(nextLink.run('targetCodec.value'), 'AV1')
  }
})
