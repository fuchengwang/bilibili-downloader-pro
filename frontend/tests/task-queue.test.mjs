import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import ts from 'typescript'
import { ref, createSSRApp } from 'vue'

const source = readFileSync(new URL('../src/utils/task-queue.ts', import.meta.url), 'utf8')
const js = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } }).outputText
const { orderedQueueTasks, mergeTaskSnapshots } = await import(`data:text/javascript;base64,${Buffer.from(js).toString('base64')}`)
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

const app=readFileSync(new URL('../src/App.vue',import.meta.url),'utf8')
const ast=ts.createSourceFile('App.ts',app.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1],ts.ScriptTarget.Latest,true)
const initialize=ast.statements.find(node=>ts.isFunctionDeclaration(node)&&node.name.text==='initializeAuthorizedApp')
const initializeCode=ts.transpileModule(initialize.getText(ast),{compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText
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
  vm.runInContext(initializeCode,context)
  const pending=context.initializeAuthorizedApp()
  assert.equal(typeof handlers['task:update'],'function')
  handlers['task:update'](task('A1',1,'downloading'))
  handlers['task:update'](task('A2',2))
  finish([task('A1',1)])
  await pending
  assert.deepEqual(Array.from(orderedQueueTasks(context.tasks.value),t=>[t.id,t.status]),[['A1','downloading'],['A2','queued']])
})

const vueRequire=createRequire(import.meta.resolve('vue/package.json'))
const {compileTemplate}=vueRequire('@vue/compiler-sfc')
const {renderToString}=vueRequire('@vue/server-renderer')
const component=readFileSync(new URL('../src/components/DownloadQueue.vue',import.meta.url),'utf8')
const template=compileTemplate({source:component.match(/<template>([\s\S]*?)<\/template>/)[1],filename:'DownloadQueue.vue',id:'queue-test',ssr:true,ssrCssVars:[],transformAssetUrls:false})
assert.deepEqual(template.errors,[])
const rendered=vm.createContext({exports:{},require:vueRequire})
vm.runInContext(ts.transpileModule(template.code,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText,rendered)
test('queued task renders pause-queue action while paused task renders continue',async()=>{
  for(const [status,expected,unexpected]of [['queued','暂停排队','继续'],['paused','继续','暂停排队']]){
    const tasks=[{...task('A1',1,status),partTitle:'A1',title:'A',progress:0}]
    const html=await renderToString(createSSRApp({
      ssrRender:rendered.exports.ssrRender,
      components:Object.fromEntries(['Play','Pause','RotateCcw','Trash2','FolderOpen','AlertCircle','Clock','Zap','HardDrive','Film','ExternalLink'].map(name=>[name,{render:()=>null}])),
      data:()=>({tasks,filteredTasks:tasks,currentFilter:'all',isLegacyPlaybackError:()=>false}),
    }))
    assert.ok(html.includes(`title="${expected}"`),html)
    assert.ok(!html.includes(`title="${unexpected}"`),html)
  }
})
