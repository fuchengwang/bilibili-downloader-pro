import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { ref, toRaw } from 'vue'

// Execute the real component functions with a controllable Wails response.
const component = readFileSync(new URL('../src/components/QuickParse.vue', import.meta.url), 'utf8')
const script = component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const ast = ts.createSourceFile('QuickParse.ts', script, ts.ScriptTarget.Latest, true)
const functionNames = new Set(['fetchQualities', 'handleClear', 'currentEpisode'])
const functions = ast.statements
  .filter(node => ts.isFunctionDeclaration(node) && functionNames.has(node.name.text))
  .map(node => node.getText(ast)).join('\n')
const code = ts.transpileModule(functions, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText

function harness() {
  const requests = []
  const messages = []
  const context = vm.createContext({
    ref, toRaw,
    parsedDetail: ref(null), availableQualities: ref([]),
    selectedQuality: ref('highest'), isFetchingQualities: ref(false),
    inputUrl: ref('video'), qualityRequest: 0,
    emit: (...args) => messages.push(args),
    GetAvailableQualities: (...args) => new Promise((resolve, reject) => requests.push({ args, resolve, reject })),
  })
  vm.runInContext(code, context)
  return { context, requests, messages }
}

const detail = () => ({ bvid: 'BVtest', aid: 1, type: 'video', episodes: [{ cid: 2 }] })
const qualities = [{ id: 80, label: '1080P', isAvailable: true }, { id: 64, label: '720P', isAvailable: true }]

test('collection quality lookup uses the linked episode instead of the first', async () => {
  const { context: c, requests } = harness()
  const raw = { ...detail(), defaultPage: 2, episodes: [
    { bvid: 'BVfirst', aid: 10, cid: 100 },
    { bvid: 'BVcurrent', aid: 20, cid: 200 },
  ] }
  c.parsedDetail.value = raw
  const pending = c.fetchQualities(raw, c.currentEpisode(raw))
  assert.deepEqual(requests[0].args, ['BVcurrent', 20, 200, 0, false, false])
  requests[0].resolve([{ id: 32, label: '480P', isAvailable: true }])
  await pending
  assert.equal(c.availableQualities.value[0].id, 32)
})

test('initial parse accepts quality response after Vue wraps the raw detail', async () => {
  const { context: c, requests } = harness()
  const raw = detail()
  c.parsedDetail.value = raw
  assert.notEqual(c.parsedDetail.value, raw)
  const pending = c.fetchQualities(raw, raw.episodes[0])
  requests[0].resolve(qualities)
  await pending
  assert.deepEqual(toRaw(c.availableQualities.value), qualities)
  assert.equal(c.isFetchingQualities.value, false)
})

test('classroom quality lookup preserves the purchased lesson and source type', async () => {
  const { context: c, requests } = harness()
  const raw = { bvid: '', aid: 0, type: 'cheese', defaultPage: 2, episodes: [
    { aid: 11, cid: 101, epid: 1001 },
    { aid: 22, cid: 202, epid: 2002 },
  ] }
  c.parsedDetail.value = raw
  const pending = c.fetchQualities(raw, c.currentEpisode(raw))
  assert.deepEqual(requests[0].args, ['', 22, 202, 2002, false, true])
  requests[0].resolve(qualities)
  await pending
  assert.deepEqual(toRaw(c.availableQualities.value), qualities)
})

test('login refresh accepts proxy detail and ignores previous request', async () => {
  const { context: c, requests } = harness()
  const raw = detail()
  c.parsedDetail.value = raw
  const first = c.fetchQualities(raw, raw.episodes[0])
  const refresh = c.fetchQualities(c.parsedDetail.value, c.parsedDetail.value.episodes[0])
  requests[1].resolve(qualities)
  await refresh
  requests[0].resolve([{ id: 16, isAvailable: true }])
  await first
  assert.deepEqual(toRaw(c.availableQualities.value), qualities)
})

test('response for replaced video cannot populate the new video', async () => {
  const { context: c, requests } = harness()
  const raw = detail()
  c.parsedDetail.value = raw
  const pending = c.fetchQualities(raw, raw.episodes[0])
  c.parsedDetail.value = detail()
  requests[0].resolve(qualities)
  await pending
  assert.equal(c.availableQualities.value.length, 0)
})

test('clearing input invalidates an in-flight quality request', async () => {
  const { context: c, requests } = harness()
  const raw = detail()
  c.parsedDetail.value = raw
  const pending = c.fetchQualities(raw, raw.episodes[0])
  c.handleClear()
  requests[0].resolve(qualities)
  await pending
  assert.equal(c.parsedDetail.value, null)
  assert.equal(c.availableQualities.value.length, 0)
  assert.equal(c.isFetchingQualities.value, false)
})

test('API failure displays an error and ends loading', async () => {
  const { context: c, requests, messages } = harness()
  const raw = detail()
  c.parsedDetail.value = raw
  const pending = c.fetchQualities(raw, raw.episodes[0])
  requests[0].reject(new Error('network failure'))
  await pending
  assert.equal(c.availableQualities.value.length, 0)
  assert.equal(c.isFetchingQualities.value, false)
  assert.match(messages[0][1], /network failure/)
})
