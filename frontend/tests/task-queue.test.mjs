import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import ts from 'typescript'
import { ref, computed, createSSRApp } from 'vue'

const source = readFileSync(new URL('../src/utils/task-queue.ts', import.meta.url), 'utf8')
const js = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } }).outputText
const {
  orderedQueueTasks,
  mergeTaskSnapshots,
  isPausableQueueTask,
  isResumableQueueTask,
  pruneSelectedQueueIds,
  pruneSelectedHistoryIds,
  parseSizeStringToBytes,
  formatHistoryTotalSize,
  toggleQueueTaskSelection,
  summarizeQueueSelection,
  moveQueueTasks
} = await import(`data:text/javascript;base64,${Buffer.from(js).toString('base64')}`)
const task = (id, queueOrder, status = 'queued') => ({ id, queueOrder, status, createdAt: 10 })

test('queue uses persistent admission order and leaves newest-first history untouched', () => {
  const history = [task('B2',5),task('B1',4),task('A3',3),task('A2',2,'paused'),task('A1',1)]
  assert.deepEqual(orderedQueueTasks(history).map(t=>t.id),['A1','A2','A3','B1','B2'])
  assert.deepEqual(history.map(t=>t.id),['B2','B1','A3','A2','A1'])
  history[2].status='completed'
  assert.deepEqual(orderedQueueTasks(history).map(t=>t.id),['A1','A2','B1','B2'])
})

test('legacy snapshots with equal creation timestamps keep original insertion order', () => {
  assert.deepEqual(orderedQueueTasks([task('A3'),task('A2'),task('A1')]).map(t=>t.id),['A1','A2','A3'])
})

test('events received during initial read override stale statuses without losing new tasks', () => {
  const merged=mergeTaskSnapshots([task('A2',2),task('A1',1)],[task('A1',1,'downloading'),task('B1',3),task('B1',3,'paused')])
  assert.equal(merged.find(t=>t.id==='A1').status,'downloading')
  assert.equal(merged.find(t=>t.id==='B1').status,'paused')
  assert.equal(merged.length,3)
  assert.deepEqual(orderedQueueTasks(merged).map(t=>t.id),['A1','A2','B1'])
})

test('toggleQueueTaskSelection toggles single items and supports forward/backward Shift range selection', () => {
  const visible = ['T1', 'T2', 'T3', 'T4', 'T5']
  const first = toggleQueueTaskSelection(new Set(), visible, 'T2')
  assert.deepEqual([...first.selected], ['T2'])
  assert.equal(first.anchorId, 'T2')

  const forward = toggleQueueTaskSelection(first.selected, visible, 'T4', { shiftKey: true, anchorId: first.anchorId })
  assert.deepEqual([...forward.selected].sort(), ['T2', 'T3', 'T4'])
  assert.equal(forward.anchorId, 'T2')

  const backward = toggleQueueTaskSelection(new Set(['T4']), visible, 'T1', { shiftKey: true, anchorId: 'T4' })
  assert.deepEqual([...backward.selected].sort(), ['T1', 'T2', 'T3', 'T4'])

  const deselected = toggleQueueTaskSelection(forward.selected, visible, 'T3')
  assert.deepEqual([...deselected.selected].sort(), ['T2', 'T4'])
  assert.equal(deselected.anchorId, 'T3')
})

test('summarizeQueueSelection classifies mixed task statuses into pausable, resumable, and deletable IDs', () => {
  const visible = [
    task('D1', 1, 'downloading'),
    task('Q1', 2, 'queued'),
    task('P1', 3, 'paused'),
    task('E1', 4, 'error'),
    task('C1', 5, 'cancelled'),
    task('M1', 6, 'merging')
  ]
  assert.equal(isPausableQueueTask(visible[0]), true)
  assert.equal(isPausableQueueTask(visible[1]), true)
  assert.equal(isPausableQueueTask(visible[5]), false)
  assert.equal(isResumableQueueTask(visible[2]), true)
  assert.equal(isResumableQueueTask(visible[3]), true)
  assert.equal(isResumableQueueTask(visible[4]), true)
  assert.equal(isResumableQueueTask(visible[0]), false)

  const partial = summarizeQueueSelection(visible, new Set(['D1', 'Q1', 'P1', 'E1', 'M1']))
  assert.deepEqual(partial.pausableIds, ['D1', 'Q1'])
  assert.deepEqual(partial.resumableIds, ['P1', 'E1'])
  assert.deepEqual(partial.deletableIds, ['D1', 'Q1', 'P1', 'E1', 'M1'])
  assert.equal(partial.selectedCount, 5)
  assert.equal(partial.totalCount, 6)
  assert.equal(partial.allSelected, false)
  assert.equal(partial.someSelected, true)

  const full = summarizeQueueSelection(visible, new Set(visible.map(t => t.id)))
  assert.equal(full.allSelected, true)
  assert.equal(full.someSelected, false)
  assert.deepEqual(full.resumableIds, ['P1', 'E1', 'C1'])
})

test('pruneSelectedQueueIds drops completed and removed tasks while preserving active selections', () => {
  const tasks = [task('A1', 1, 'paused'), task('A2', 2, 'completed'), task('A3', 3, 'downloading')]
  const pruned = pruneSelectedQueueIds(new Set(['A1', 'A2', 'A3', 'MISSING']), tasks)
  assert.deepEqual([...pruned], ['A1', 'A3'])
})

const app=readFileSync(new URL('../src/App.vue',import.meta.url),'utf8')
const ast=ts.createSourceFile('App.ts',app.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1],ts.ScriptTarget.Latest,true)
function compileFunctions(script, ...names) {
  const source = names.map(name => script.statements.find(node =>
    ts.isFunctionDeclaration(node) && node.name?.text === name
  ).getText(script)).join('\n')
  return ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
}

test('real application subscribes before reading tasks and merges updates arriving during IPC',async()=>{
  const handlers={}
  let finish
  const context=vm.createContext({
    appInitialized:false,tasks:ref([]),mergeTaskSnapshots,
    EventsOn:(name,fn)=>{handlers[name]=fn},
    GetTasks:()=>new Promise(resolve=>{finish=resolve}),
    window:{addEventListener(){}},nextTick(){},checkAndAutoParseClipboard(){},console:{error(){}},
    activeTab:ref('queue'),unreadCompletedCount:ref(0),showToast(){},triggerConfetti(){},
  })
  vm.runInContext(compileFunctions(ast, 'initializeAuthorizedApp'), context)
  const pending=context.initializeAuthorizedApp()
  assert.equal(typeof handlers['task:update'],'function')
  handlers['task:update'](task('A1',1,'downloading'))
  handlers['task:update'](task('A2',2))
  finish([task('A1',1)])
  await pending
  assert.deepEqual(Array.from(orderedQueueTasks(context.tasks.value),t=>[t.id,t.status]),[['A1','downloading'],['A2','queued']])
})

test('whole-queue controls show pending state and ignore competing clicks until the RPC settles', async () => {
  const calls = []
  let finish
  const context = vm.createContext({
    queueBatchBusy: ref(false), queueAllAction: ref(null), showToast() {},
    PauseAllTasks: () => new Promise(resolve => { calls.push('pause'); finish = resolve }),
    ResumeAllTasks: async () => { calls.push('resume') },
  })
  vm.runInContext(compileFunctions(ast, 'runAllQueueAction'), context)
  const pending = context.runAllQueueAction('pause')
  assert.equal(context.queueAllAction.value, 'pause')
  assert.equal(context.queueBatchBusy.value, true)
  await context.runAllQueueAction('resume')
  assert.deepEqual(calls, ['pause'])
  finish()
  await pending
  assert.equal(context.queueAllAction.value, null)
  await context.runAllQueueAction('resume')
  assert.deepEqual(calls, ['pause', 'resume'])
})

test('failed queue changes preserve task events and release the controls for retry', async () => {
  for (const [method, binding, args] of [
    ['onMoveQueueTasks', 'ReorderTasks', [['B'], { type: 'up' }]],
    ['onPrioritizeQueueTasks', 'PrioritizeTasks', [['B']]]
  ]) {
    const handlers = {}
    const calls = []
    const toasts = []
    let rejectChange
    const context = vm.createContext({
      appInitialized: false, tasks: ref([]), queueBatchBusy: ref(false),
      mergeTaskSnapshots, moveQueueTasks, orderedQueueTasks,
      EventsOn: (name, handler) => { handlers[name] = handler },
      GetTasks: async () => [task('A', 1, 'downloading'), task('B', 2)],
      [binding]: ids => {
        calls.push(Array.from(ids))
        return new Promise((_, reject) => { rejectChange = reject })
      },
      window: { addEventListener() {} }, nextTick() {}, checkAndAutoParseClipboard() {},
      showToast: (message, type) => toasts.push({ message, type })
    })
    vm.runInContext(compileFunctions(ast, 'initializeAuthorizedApp', method), context)
    await context.initializeAuthorizedApp()

    const pending = context[method](...args)
    assert.equal(context.queueBatchBusy.value, true)
    assert.deepEqual(calls, [binding === 'ReorderTasks' ? ['B', 'A'] : ['B']])
    assert.deepEqual(orderedQueueTasks(context.tasks.value).map(t => t.id), ['A', 'B'])
    await context[method](...args)
    assert.equal(calls.length, 1)

    handlers['task:update'](task('A', 1, 'completed'))
    handlers['task:update'](task('C', 3))
    rejectChange(new Error('save failed'))
    await pending
    assert.deepEqual(context.tasks.value.map(t => [t.id, t.status]), [
      ['C', 'queued'], ['A', 'completed'], ['B', 'queued']
    ])
    assert.equal(context.queueBatchBusy.value, false)
    assert.equal(toasts[0].type, 'error')
  }
})

const vueRequire=createRequire(import.meta.resolve('vue/package.json'))
const {compileTemplate}=vueRequire('@vue/compiler-sfc')
const {renderToString}=vueRequire('@vue/server-renderer')
const component=readFileSync(new URL('../src/components/DownloadQueue.vue',import.meta.url),'utf8')
const template=compileTemplate({source:component.match(/<template>([\s\S]*?)<\/template>/)[1],filename:'DownloadQueue.vue',id:'queue-test',ssr:true,ssrCssVars:[],transformAssetUrls:false})
assert.deepEqual(template.errors,[])
const rendered=vm.createContext({exports:{},require:vueRequire})
vm.runInContext(ts.transpileModule(template.code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText,rendered)

const iconStubs = Object.fromEntries(
  ['Download', 'Pause', 'RotateCcw', 'Trash2', 'FolderOpen', 'AlertCircle', 'AlertTriangle', 'Clock', 'Zap', 'HardDrive', 'Film', 'ExternalLink', 'ListChecks', 'Check', 'Minus', 'X', 'LayoutList', 'List', 'GripVertical', 'ArrowUpToLine', 'ArrowDownToLine', 'ArrowUp', 'ArrowDown']
    .map(name => [name, { render: () => null }])
)

async function renderQueue({ tasks, filteredTasks = tasks, currentFilter = 'all', viewMode = 'comfortable', selectionMode = false, selectedIds = new Set(), batchBusy = false, showBatchDeleteModal = false, showDeleteModal = false, deleteTargetTask = null }) {
  const activeQueueCount = tasks.filter(t => t.status !== 'completed').length
  const downloadingCount = tasks.filter(t => t.status === 'downloading' || t.status === 'merging').length
  const pausedCount = tasks.filter(t => t.status === 'paused' || t.status === 'queued').length
  const errorCount = tasks.filter(t => t.status === 'error' || t.status === 'cancelled').length
  const ordered = orderedQueueTasks(tasks)
  const selectionSummary = summarizeQueueSelection(filteredTasks, selectedIds, ordered)
  const posMap = new Map(ordered.map((t, idx) => [t.id, idx + 1]))
  const queueContext = vm.createContext({ getQueuePosition: id => posMap.get(id) || 1 })
  const queueScript = ts.createSourceFile('DownloadQueue.ts', component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1], ts.ScriptTarget.Latest, true)
  vm.runInContext(compileFunctions(queueScript, 'formatDeleteTargetStatus', 'canPrioritizeSingleTask'), queueContext)
  return renderToString(createSSRApp({
    ssrRender: rendered.exports.ssrRender,
    components: iconStubs,
    data: () => ({
      tasks,
      filteredTasks,
      currentFilter,
      viewMode,
      activeQueueCount,
      downloadingCount,
      pausedCount,
      errorCount,
      selectionMode,
      selectionSummary,
      canPrioritizeSelection: selectionSummary.canMoveTop || selectionSummary.selectedTasks.some(t => t.status === 'queued'),
      batchDeletePreviewTasks: selectionSummary.selectedTasks.slice(0, 4),
      batchBusy,
      showBatchDeleteModal,
      showDeleteModal,
      deleteTargetTask,
      formatDeleteTargetStatus: queueContext.formatDeleteTargetStatus,
      openDeleteModal: () => {},
      closeDeleteModal: () => {},
      confirmDeleteSingle: () => {},
      isDragging: false,
      dropTargetId: null,
      dropPosition: 'before',
      draggingTaskIds: [],
      dragPointerX: 0,
      dragPointerY: 0,
      dropTargetLabel: '',
      getQueuePosition: queueContext.getQueuePosition,
      canPrioritizeSingleTask: queueContext.canPrioritizeSingleTask,
      isTaskBeingDragged: () => false,
      isTaskSelected: id => selectedIds.has(id),
      isLegacyPlaybackError: () => false
    })
  }))
}

test('queued task renders pause-queue action while paused task renders continue',async()=>{
  for(const [status,expected,unexpected]of [['queued','暂停排队','继续下载'],['paused','继续下载','暂停排队']]){
    const tasks=[{...task('A1',1,status),partTitle:'A1',title:'A',progress:0}]
    const html=await renderQueue({ tasks })
    assert.ok(html.includes(`title="${expected}"`),html)
    assert.ok(!html.includes(`title="${unexpected}"`),html)
  }
})

test('queued task at the head exposes priority while another task is downloading', async () => {
  const tasks = [task('A1', 1, 'queued'), task('A2', 2, 'downloading')]
  const html = await renderQueue({ tasks })
  const actions = [...html.matchAll(/<button[^>]+title="置顶优先下载"[^>]*>/g)].map(match => match[0])
  assert.equal(actions.length, 2)
  assert.match(actions[0], /\bis-queued-action\b/)
  assert.doesNotMatch(actions[0], /\b(?:disabled|is-hidden-action)\b/)
})

test('normal mode shows multi-select trigger without batch bar; selection mode renders batch bar and checkboxes', async () => {
  const tasks = [
    { ...task('A1', 1, 'downloading'), partTitle: 'Part 1', title: 'Video A', progress: 40 },
    { ...task('A2', 2, 'paused'), partTitle: 'Part 2', title: 'Video A', progress: 15 },
    { ...task('A3', 3, 'error'), partTitle: 'Part 3', title: 'Video A', progress: 0, errorMsg: 'boom' }
  ]

  const normalHtml = await renderQueue({ tasks, selectionMode: false })
  assert.ok(normalHtml.includes('多选'), normalHtml)
  assert.ok(normalHtml.includes('失败 (1)'), normalHtml)
  assert.ok(!normalHtml.includes('class="batch-bar"'), normalHtml)
  assert.ok(!normalHtml.includes('role="checkbox"'), normalHtml)

  const selectedHtml = await renderQueue({
    tasks,
    selectionMode: true,
    selectedIds: new Set(['A1', 'A2'])
  })
  assert.ok(selectedHtml.includes('退出多选'), selectedHtml)
  assert.ok(selectedHtml.includes('class="batch-bar"'), selectedHtml)
  assert.ok(selectedHtml.includes('role="checkbox"'), selectedHtml)
  assert.ok(selectedHtml.includes('优先置顶'), selectedHtml)
  assert.ok(selectedHtml.includes('继续 (1)'), selectedHtml)
  assert.ok(selectedHtml.includes('暂停 (1)'), selectedHtml)
  assert.ok(selectedHtml.includes('删除 (2)'), selectedHtml)
})

test('moveQueueTasks supports top, bottom, step up, step down, and drag-drop before/after', () => {
  const initial = [
    task('T1', 10, 'downloading'),
    task('T2', 20, 'queued'),
    task('T3', 30, 'paused'),
    task('T4', 40, 'queued'),
    task('DONE', 5, 'completed')
  ]

  const movedTop = moveQueueTasks(initial, ['T3', 'T4'], { type: 'top' })
  assert.deepEqual(orderedQueueTasks(movedTop).map(t => [t.id, t.queueOrder]), [
    ['T3', 10],
    ['T4', 20],
    ['T1', 30],
    ['T2', 40]
  ])

  const movedBottom = moveQueueTasks(initial, ['T1', 'T3'], { type: 'bottom' })
  assert.deepEqual(orderedQueueTasks(movedBottom).map(t => t.id), ['T2', 'T4', 'T1', 'T3'])

  const movedUp = moveQueueTasks(initial, ['T2', 'T4'], { type: 'up' })
  assert.deepEqual(orderedQueueTasks(movedUp).map(t => t.id), ['T2', 'T1', 'T4', 'T3'])

  const movedDown = moveQueueTasks(initial, ['T1', 'T3'], { type: 'down' })
  assert.deepEqual(orderedQueueTasks(movedDown).map(t => t.id), ['T2', 'T1', 'T4', 'T3'])

  const droppedBefore = moveQueueTasks(initial, ['T4'], { type: 'drop', targetId: 'T2', position: 'before' })
  assert.deepEqual(orderedQueueTasks(droppedBefore).map(t => t.id), ['T1', 'T4', 'T2', 'T3'])

  const droppedAfter = moveQueueTasks(initial, ['T1', 'T2'], { type: 'drop', targetId: 'T3', position: 'after' })
  assert.deepEqual(orderedQueueTasks(droppedAfter).map(t => t.id), ['T3', 'T1', 'T2', 'T4'])
})

test('filtered step changes the visible order while keeping hidden tasks in their slots', () => {
  const initial = [task('A', 1), task('X', 2, 'downloading'), task('B', 3)]
  const visible = [initial[0], initial[2]]
  const visibleIds = visible.map(t => t.id)
  assert.equal(summarizeQueueSelection(visible, new Set(['B']), initial).canMoveUp, true)
  assert.equal(summarizeQueueSelection(visible, new Set(['A']), initial).canMoveUp, false)

  for (const [id, type] of [['B', 'up'], ['A', 'down']]) {
    const moved = moveQueueTasks(initial, [id], { type, visibleIds })
    assert.deepEqual(orderedQueueTasks(moved).map(t => t.id), ['B', 'X', 'A'])
  }

  const allVisibleSelected = summarizeQueueSelection(visible, new Set(['A', 'B']), initial)
  assert.equal(allVisibleSelected.canMoveUp, false)
  assert.equal(allVisibleSelected.canMoveDown, false)
  assert.equal(allVisibleSelected.canMoveTop, true)
  assert.equal(allVisibleSelected.canMoveBottom, true)
})

test('pointer cancellation clears the drag without sending a queue change', () => {
  const script = ts.createSourceFile('DownloadQueue.ts',
    component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1], ts.ScriptTarget.Latest, true)
  const listeners = new Map()
  const emitted = []
  const context = vm.createContext({
    window: {
      addEventListener: (name, listener) => listeners.set(name, listener),
      removeEventListener: (name, listener) => {
        assert.equal(listeners.get(name), listener)
        listeners.delete(name)
      }
    },
    document: { body: { style: {} } },
    emit: (...args) => emitted.push(args),
    props: { batchBusy: false }, filteredTasks: ref([task('A', 1), task('B', 2)]),
    dragPendingTaskId: null, dragStartX: 0, dragStartY: 0,
    dragPointerX: ref(0), dragPointerY: ref(0),
    isDragging: ref(false), draggingTaskIds: ref([]), dropTargetId: ref(null), dropPosition: ref('before'),
    resolveDragGroupIds: id => [id],
    updateDropTargetFromPointer: () => { context.dropTargetId.value = 'A' }
  })
  vm.runInContext(compileFunctions(script, 'startTaskDrag', 'handleGlobalPointerMove', 'handleGlobalPointerUp', 'resetTaskDrag'), context)

  for (const event of ['pointercancel', 'pointerup']) {
    context.startTaskDrag('B', { button: 0, clientX: 0, clientY: 0, preventDefault() {}, stopPropagation() {} })
    listeners.get('pointermove')({ clientX: 0, clientY: 20 })
    assert.equal(context.dropTargetId.value, 'A')
    listeners.get(event)()
    assert.equal(context.isDragging.value, false)
    assert.equal(context.dropTargetId.value, null)
    assert.equal(listeners.size, 0)
    assert.equal(emitted.length, event === 'pointerup' ? 1 : 0)
  }
  assert.deepEqual(JSON.parse(JSON.stringify(emitted)), [['move-tasks', ['B'], { type: 'drop', targetId: 'A', position: 'before' }]])
})

test('parseSizeStringToBytes and formatHistoryTotalSize format MB below 1GB and GB at or above 1GB', () => {
  assert.equal(parseSizeStringToBytes('20.68 MB'), Math.round(20.68 * 1024 * 1024))
  assert.equal(parseSizeStringToBytes('1.5 GB / 2.0 GB'), Math.round(1.5 * 1024 * 1024 * 1024))
  assert.equal(parseSizeStringToBytes(''), 0)

  const mbOnly = [
    { totalBytes: 500 * 1024 * 1024 },
    { sizeStr: '20.5 MB' }
  ]
  assert.equal(formatHistoryTotalSize(mbOnly), '520.5 MB')

  const gbAndFallback = [
    { totalBytes: 1024 * 1024 * 1024 },
    { downloadedBytes: 512 * 1024 * 1024 }
  ]
  assert.equal(formatHistoryTotalSize(gbAndFallback), '1.50 GB')
  assert.equal(formatHistoryTotalSize([]), '')
})

test('pruneSelectedHistoryIds keeps only completed tasks present in list', () => {
  const tasks = [task('H1', 1, 'completed'), task('H2', 2, 'paused'), task('H3', 3, 'completed')]
  const pruned = pruneSelectedHistoryIds(new Set(['H1', 'H2', 'H3', 'MISSING']), tasks)
  assert.deepEqual([...pruned], ['H1', 'H3'])
})

test('single task delete modal highlights progress for downloading and paused tasks', async () => {
  for (const [status, statusLabel] of [['downloading', '下载中 · 90%'], ['paused', '已暂停 · 90%']]) {
    const target = { ...task('T90', 1, status), title: '长视频合集', partTitle: 'P1 关键章节', progress: 90 }
    const html = await renderQueue({
      tasks: [target],
      showDeleteModal: true,
      deleteTargetTask: target
    })
    assert.ok(html.includes('删除下载任务'), html)
    assert.ok(html.includes('当前已下载'), html)
    assert.ok(html.includes('90%'), html)
    assert.ok(html.includes('长视频合集'), html)
    assert.ok(html.includes('P1 关键章节'), html)
    assert.ok(html.includes(statusLabel), html)
  }
})

test('single task delete requires confirmation and aborts if task completes before confirm', () => {
  const script = ts.createSourceFile('DownloadQueue.ts',
    component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1], ts.ScriptTarget.Latest, true)
  const emitted = []
  const props = { tasks: ref([{ ...task('D1', 1, 'downloading'), progress: 90 }]) }
  const context = vm.createContext({
    ref,
    computed,
    props: { get tasks() { return props.tasks.value } },
    showDeleteModal: ref(false),
    deleteTargetId: ref(null),
    emit: (...args) => emitted.push(args)
  })
  const deleteTargetDecl = script.statements.find(node =>
    ts.isVariableStatement(node) && node.declarationList.declarations[0]?.name?.getText(script) === 'deleteTargetTask'
  ).getText(script)
  const deleteTargetJs = ts.transpileModule(deleteTargetDecl, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  vm.runInContext(deleteTargetJs + '\n' + compileFunctions(script, 'openDeleteModal', 'closeDeleteModal', 'confirmDeleteSingle'), context)

  context.openDeleteModal(props.tasks.value[0])
  assert.equal(context.showDeleteModal.value, true)
  assert.equal(emitted.length, 0)

  context.closeDeleteModal()
  assert.equal(context.showDeleteModal.value, false)
  assert.equal(emitted.length, 0)

  context.openDeleteModal(props.tasks.value[0])
  context.confirmDeleteSingle()
  assert.equal(context.showDeleteModal.value, false)
  assert.deepEqual(JSON.parse(JSON.stringify(emitted)), [['delete-task', 'D1', false]])

  emitted.length = 0
  context.openDeleteModal(props.tasks.value[0])
  props.tasks.value = [{ ...task('D1', 1, 'completed'), progress: 100 }]
  context.confirmDeleteSingle()
  assert.equal(context.showDeleteModal.value, false)
  assert.equal(emitted.length, 0)
})
