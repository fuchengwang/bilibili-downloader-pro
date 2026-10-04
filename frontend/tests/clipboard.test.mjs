import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { ref } from 'vue'

const component = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')
const script = component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true)
const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name.text === 'checkAndAutoParseClipboard')
const code = ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText

function harness(text) {
  const parsed = []
  const context = vm.createContext({
    settings: ref({ autoClipboard: true }), isCheckingClipboard: false, lastParsedClipboardText: '',
    activeTab: ref('queue'), quickParseRef: ref({ setAndParse: value => parsed.push(value) }),
    ReadClipboard: async () => text, nextTick: async () => {}, showToast: () => {},
  })
  vm.runInContext(code, context)
  return { context, parsed }
}

test('classroom clipboard link opens the parse view and is dispatched once', async () => {
  const url = 'https://www.bilibili.com/cheese/play/ep2210114?csource=common_channelclass_watchedrecord_null'
  const { context: c, parsed } = harness(url)
  await c.checkAndAutoParseClipboard()
  await c.checkAndAutoParseClipboard()
  assert.deepEqual(parsed, [url])
  assert.equal(c.activeTab.value, 'parse')
})

test('clipboard recognition waits until the parse view exists before consuming a link', async () => {
  const { context: c, parsed } = harness('https://www.bilibili.com/cheese/play/ss802763862')
  c.quickParseRef.value = null
  await c.checkAndAutoParseClipboard()
  assert.equal(c.lastParsedClipboardText, '')
  c.quickParseRef.value = { setAndParse: text => parsed.push(text) }
  await c.checkAndAutoParseClipboard()
  assert.equal(parsed.length, 1)
})

test('disabled clipboard setting preserves user preference', async () => {
  const { context: c, parsed } = harness('https://www.bilibili.com/cheese/play/ep2210114')
  c.settings.value.autoClipboard = false
  await c.checkAndAutoParseClipboard()
  assert.equal(parsed.length, 0)
  assert.equal(c.activeTab.value, 'queue')
})
