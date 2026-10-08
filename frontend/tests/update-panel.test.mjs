import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { ref, reactive, computed, watch, nextTick } from 'vue'

const component = readFileSync(new URL('../src/components/UpdatePanel.vue', import.meta.url), 'utf8')
const script = component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const ast = ts.createSourceFile('UpdatePanel.ts', script, ts.ScriptTarget.Latest, true)
const statements = ast.statements.filter(s => !ts.isImportDeclaration(s)).map(s => s.getText(ast)).join('\n')
const code = ts.transpileModule(statements, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
function harness() {
  const state = reactive({autoCheck: true, currentVersion: '1.0.0', version: '', notes: '', phase: 'idle', error: '', downloaded: 0, total: 0, hasUpdate: false})
  const choices = []
  const context = vm.createContext({ref, reactive, computed, watch, defineProps: () => ({state}),
    SetAutomaticUpdateCheck: async choice => { choices.push(choice); state.autoCheck = choice },
    CheckForUpdates: async () => {}, DownloadUpdate: async () => {}, PauseUpdateDownload: async () => {}, RetryUpdateDownload: async () => {}, RestartForUpdate: async () => {},
  })
  vm.runInContext(code + '\nglobalThis.ui = {act, changeAutomatic, progress, status, error, dismissed, requesting, ready}', context)
  return { state, choices, context, ui: context.ui }
}
test('automatic check is one checkbox, with no interval wording', async () => {
  const {state, choices, ui} = harness()
  await ui.changeAutomatic({target:{checked:false}})
  assert.deepEqual(choices, [false]);assert.equal(state.autoCheck, false)
  assert.match(component, /<span>自动检查更新<\/span>/)
  assert.doesNotMatch(component, /每周|一周|7\s*天|启动时|启动后/)
})
test('failed choice save restores checkbox and can be retried', async () => {
  const {context, ui} = harness()
  context.SetAutomaticUpdateCheck = async () => {throw new Error('保存失败')}
  const target = {checked:false};await ui.changeAutomatic({target})
  assert.equal(target.checked, true);assert.equal(ui.error.value, '保存失败')
})
test('repeated action cannot start parallel operations; failure releases UI', async () => {
  const {ui} = harness();let release;let calls=0
  const pending = ui.act(() => {calls++;return new Promise(resolve => {release=resolve})})
  await ui.act(async () => {calls++});assert.equal(calls, 1)
  release();await pending;assert.equal(ui.requesting.value, false)
  await ui.act(async () => {throw new Error('断网')});assert.equal(ui.error.value, '断网');assert.equal(ui.requesting.value, false)
  await ui.act(async () => {calls++});assert.equal(calls, 2);assert.equal(ui.error.value, '')
})
test('progress handles resume, partial counts and completed preparation', () => {
  const {state,ui} = harness();state.total=100;state.downloaded=35;state.phase='downloading'
  assert.equal(ui.progress.value, 35);assert.equal(ui.status.value, '正在下载更新')
  state.phase='paused';assert.equal(ui.status.value, '下载已暂停')
  state.downloaded=105;state.phase='preparing';assert.equal(ui.progress.value, 100);assert.equal(ui.status.value, '正在准备更新…')
})
test('a successful manual check has a visible result without a popup', () => {
  const {state,ui} = harness();state.checked=true
  assert.equal(ui.status.value,'未发现新版本')
  state.checked=false;state.error='连接超时'
  assert.equal(ui.status.value,'');assert.equal(ui.error.value,'连接超时')
})
test('deferring only hides the notice and keeps a ready update accessible', async () => {
  const {state,ui} = harness();state.version='1.1.0';state.phase='ready';await nextTick()
  ui.dismissed.value=true;state.error='';await nextTick();assert.equal(ui.dismissed.value, true);assert.equal(state.phase,'ready')
  state.version='1.2.0';await nextTick();assert.equal(ui.dismissed.value,false)
})
test('an interrupted installation offers retry without claiming completion', () => {
  const {state, ui} = harness()
  state.hasUpdate = true; state.version = '1.1.0'; state.phase = 'install_failed'; state.error = '上次更新未完成，可以重试安装'
  assert.equal(ui.ready.value, false)
  assert.equal(ui.status.value, '上次更新未完成')
  assert.equal(ui.error.value, state.error)
  assert.match(component, /state\.phase === 'install_failed'[\s\S]*?act\(RestartForUpdate\)[\s\S]*?>重试安装</)
  assert.doesNotMatch(component, /更新完成/)
})
