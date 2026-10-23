import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { ref, watch } from 'vue'

const source = readFileSync(new URL('../src/components/SettingsView.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const ast = ts.createSourceFile('SettingsView.ts', script, ts.ScriptTarget.Latest, true)
const code = ts.transpileModule(ast.statements.filter(n =>
  (ts.isFunctionDeclaration(n) && ['restoreLimitControls', 'handleLimitToggle', 'handleLimitUnitChange', 'cancelSaveNotice', 'scheduleSaveNotice', 'handleSave', 'savePreference', 'scheduleLimitSave', 'saveDirectory', 'scheduleDirectorySave'].includes(n.name.text))
  || (ts.isVariableStatement(n) && ['form', 'limitEnabled', 'limitUnit', 'isSaving', 'saveStatus', 'downloadLimitInput', 'limitError', 'saveQueue', 'pendingSaves', 'limitTimer', 'directoryTimer', 'lastSavedSettings', 'editVersions', 'saveNoticeTimer', 'savedSinceNotice'].includes(n.declarationList.declarations[0].name.getText(ast))))
  .map(n => n.getText(ast)).join('\n'), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText

function harness(settings = {}, fail = false, editInKB = true) {
  const saved = [], events = []
  let persisted
  let activeWrites = 0, peakWrites = 0
  const c = vm.createContext({
    ref, watch, setTimeout, clearTimeout, props: { initialSettings: settings },
    GetSettings: async () => ({ ...persisted }),
    SaveSettings: async s => {
      activeWrites++; peakWrites = Math.max(peakWrites, activeWrites)
      try {
        if (typeof fail === 'function') await fail(s)
        else if (fail) throw Error('disk unavailable')
        persisted = { ...s }; saved.push({ ...s })
      } finally { activeWrites-- }
    },
    emit: (...args) => events.push(args),
  })
  vm.runInContext(code, c)
  persisted = { ...vm.runInContext('form.value', c) }
  if (editInKB) vm.runInContext("limitEnabled.value = true; limitUnit.value = 'KB'; downloadLimitInput.value = String(form.value.downloadLimitKB)", c)
  return { c, saved, events, peak: () => peakWrites, persisted: () => persisted, form: vm.runInContext('form.value', c), input: vm.runInContext('downloadLimitInput', c) }
}

test('old and new settings default to unlimited and saving preserves all other preferences', async () => {
  const { c, saved, form, input } = harness({ maxConcurrent: 6, threadsPerTask: 8, defaultCodec: 'HEVC', autoMerge: false })
  assert.equal(form.downloadLimitKB, 0)
  input.value = '2048'
  await c.handleSave()
  assert.equal(saved[0].downloadLimitKB, 2048)
  assert.equal(saved[0].maxConcurrent, 6)
  assert.equal(saved[0].threadsPerTask, 8)
  assert.equal(saved[0].defaultCodec, 'HEVC')
  assert.equal(saved[0].autoMerge, false)
  vm.runInContext('limitEnabled.value = false', c)
  await c.handleLimitToggle()
  assert.equal(saved[1].downloadLimitKB, 0)
})

for (const invalid of ['', 0, -1, NaN, Infinity, 1048577, '10abc', '1KB', '1e3']) {
  test(`invalid speed ${String(invalid)} cannot silently change the saved limit`, async () => {
    const { c, saved, input, events } = harness()
    input.value = String(invalid)
    await c.handleSave()
    assert.equal(saved.length, 0)
    assert.ok(vm.runInContext('limitError.value', c).includes('请输入'))
    assert.ok(!events.some(([name]) => name === 'settings-saved'))
  })
}

test('a failed save does not report success or leave the settings UI busy', async () => {
  const { c, events, input } = harness({ downloadLimitKB: 1024 }, true)
  input.value = '2048'
  await c.handleSave()
  assert.ok(!events.some(([name]) => name === 'settings-saved'))
  assert.equal(events[0][2], 'error')
  assert.equal(vm.runInContext('isSaving.value', c), false)
})


test('rapid edits serialize writes and preserve each changed preference', async () => {
  let release
  let first = true
  const { c, form, persisted, peak } = harness({}, async () => {
    if (first) { first = false; await new Promise(resolve => { release = resolve }) }
  })
  form.defaultQuality = '80'
  const quality = c.savePreference('defaultQuality', '80')
  await new Promise(resolve => setImmediate(resolve))
  form.defaultCodec = 'HEVC'
  const codec = c.savePreference('defaultCodec', 'HEVC')
  form.maxConcurrent = 6
  const concurrency = c.savePreference('maxConcurrent', 6)
  form.theme = 'light'
  const theme = c.savePreference('theme', 'light')
  release()
  await Promise.all([quality, codec, concurrency, theme])
  assert.equal(peak(), 1)
  assert.equal(persisted().defaultQuality, '80')
  assert.equal(persisted().defaultCodec, 'HEVC')
  assert.equal(persisted().maxConcurrent, 6)
  assert.equal(persisted().theme, 'light')
})

test('a failed edit rolls back without preventing the next preference saving', async () => {
  let first = true
  const { c, form, persisted, events } = harness({}, async () => {
    if (first) { first = false; throw Error('disk unavailable') }
  })
  form.defaultQuality = '80'
  await c.savePreference('defaultQuality', '80')
  assert.equal(form.defaultQuality, 'highest')
  form.defaultCodec = 'AVC'
  await c.savePreference('defaultCodec', 'AVC')
  assert.equal(persisted().defaultCodec, 'AVC')
  assert.equal(events.filter(([name, , type]) => name === 'show-toast' && type === 'error').length, 1)
})

test('typing a speed limit coalesces intermediate values and saves the final value automatically', async () => {
  const { c, input, saved, persisted } = harness()
  input.value = '2'; c.scheduleLimitSave()
  input.value = '20'; c.scheduleLimitSave()
  input.value = '2048'; c.scheduleLimitSave()
  assert.equal(saved.length, 0)
  await new Promise(resolve => setTimeout(resolve, 560))
  assert.equal(saved.length, 1)
  assert.equal(persisted().downloadLimitKB, 2048)
})

test('a failed in-flight speed save preserves newer text still waiting on the typing delay', async () => {
  let release
  let first = true
  const { c, input, persisted } = harness({}, async () => {
    if (first) {
      first = false
      await new Promise(resolve => { release = resolve })
      throw Error('disk unavailable')
    }
  })
  input.value = '2048'
  const pending = c.handleSave()
  await new Promise(resolve => setImmediate(resolve))
  input.value = '4096'
  c.scheduleLimitSave()
  release()
  await pending
  assert.equal(input.value, '4096')
  await new Promise(resolve => setTimeout(resolve, 560))
  assert.equal(persisted().downloadLimitKB, 4096)
})


test('successful rapid edits show one visible automatic-save notification', async () => {
  const { c, events } = harness()
  await Promise.all([
    c.savePreference('defaultQuality', '80'),
    c.savePreference('defaultCodec', 'HEVC'),
    c.savePreference('maxConcurrent', 6),
  ])
  assert.equal(events.filter(([name]) => name === 'show-toast').length, 0)
  await new Promise(resolve => setTimeout(resolve, 660))
  const notices = events.filter(([name, , type]) => name === 'show-toast' && type === 'success')
  assert.equal(notices.length, 1)
  assert.equal(notices[0][1], '偏好设置已自动保存')
})

test('failed or unchanged preferences never show a successful-save notification', async () => {
  for (const failed of [false, true]) {
    const { c, events } = harness({}, failed)
    await c.savePreference('defaultQuality', failed ? '80' : 'highest')
    await new Promise(resolve => setTimeout(resolve, 660))
    assert.equal(events.filter(([name, , type]) => name === 'show-toast' && type === 'success').length, 0)
  }
})


test('the default limit is off, and enabling offers a usable MB/s value', async () => {
  const { c, persisted, input } = harness({}, false, false)
  assert.equal(vm.runInContext('limitEnabled.value', c), false)
  assert.equal(vm.runInContext('limitUnit.value', c), 'MB')
  assert.equal(input.value, '2')
  vm.runInContext('limitEnabled.value = true', c)
  await c.handleLimitToggle()
  assert.equal(persisted().downloadLimitKB, 2048)
  input.value = '2.5'
  await c.handleSave()
  assert.equal(persisted().downloadLimitKB, 2560)
})

test('switching units keeps the entered number and switching off cancels pending edits', async () => {
  const { c, persisted, input } = harness({ downloadLimitKB: 10240 }, false, false)
  assert.equal(input.value, '10')
  vm.runInContext("limitUnit.value = 'KB'", c)
  await c.handleLimitUnitChange()
  assert.equal(input.value, '10')
  assert.equal(persisted().downloadLimitKB, 10)
  vm.runInContext("limitUnit.value = 'MB'", c)
  await c.handleLimitUnitChange()
  assert.equal(input.value, '10')
  assert.equal(persisted().downloadLimitKB, 10240)
  input.value = '4'
  c.scheduleLimitSave()
  vm.runInContext('limitEnabled.value = false', c)
  await c.handleLimitToggle()
  await new Promise(resolve => setTimeout(resolve, 560))
  assert.equal(persisted().downloadLimitKB, 0)
  vm.runInContext('limitEnabled.value = true', c)
  await c.handleLimitToggle()
  assert.equal(persisted().downloadLimitKB, 4096)
})

test('a failed limit toggle restores the previous enabled state and value', async () => {
  const { c, persisted } = harness({ downloadLimitKB: 2560 }, true, false)
  vm.runInContext('limitEnabled.value = false', c)
  await c.handleLimitToggle()
  assert.equal(vm.runInContext('limitEnabled.value', c), true)
  assert.equal(vm.runInContext('downloadLimitInput.value', c), '2.5')
  assert.equal(persisted().downloadLimitKB, 2560)
})

test('10 KB/s becomes 10 MB/s without replacing the input with a converted fraction', async () => {
  const { c, persisted, input } = harness({ downloadLimitKB: 10 }, false, false)
  assert.equal(input.value, '10')
  vm.runInContext("limitUnit.value = 'MB'", c)
  await c.handleLimitUnitChange()
  assert.equal(input.value, '10')
  assert.equal(persisted().downloadLimitKB, 10240)
})
