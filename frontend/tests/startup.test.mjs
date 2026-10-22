import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import ts from 'typescript'
import { createSSRApp, ref, watch } from 'vue'

const vueRequire = createRequire(import.meta.resolve('vue/package.json'))
const { compileTemplate } = vueRequire('@vue/compiler-sfc')
const { renderToString } = vueRequire('@vue/server-renderer')

const html = readFileSync(new URL('../index.html', import.meta.url), 'utf8')
const bootstrap = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)]
  .find(([, code]) => code.includes('bbdown-theme'))[1]

function appearanceContext(saved, dark = false, storageUnavailable = false, startupTheme) {
  const root = { dataset: startupTheme ? { startupTheme } : {}, style: {} }
  const backgrounds = []
  const media = { matches: dark, addEventListener: (_, callback) => { media.changed = callback } }
  const context = vm.createContext({
    document: { documentElement: root },
    localStorage: {
      getItem() { if (storageUnavailable) throw Error('storage unavailable'); return saved },
      setItem() { if (storageUnavailable) throw Error('storage unavailable') },
    },
    window: {
      matchMedia: () => media,
      runtime: { WindowSetBackgroundColour: (...rgba) => backgrounds.push(rgba) },
    },
    exports: {}, require: () => ({ ref, watch }),
  })
  return { context, root, backgrounds, media }
}

for (const [saved, dark, expected] of [
  ['light', true, 'light'], ['dark', false, 'dark'],
  ['system', false, 'light'], ['system', true, 'dark'],
  [null, false, 'dark'], ['invalid', false, 'dark'],
]) {
  test(`initial HTML uses ${expected} with saved appearance ${saved} and system dark=${dark}`, () => {
    const { context, root } = appearanceContext(saved, dark)
    vm.runInContext(bootstrap, context)
    assert.equal(root.dataset.theme, expected)
    assert.equal(root.style.colorScheme, expected)
  })
}

test('unavailable web storage cannot prevent the initial page loading', () => {
  const { context, root } = appearanceContext(null, false, true)
  vm.runInContext(bootstrap, context)
  assert.equal(root.dataset.theme, 'dark')
})

for (const cached of ['dark', null]) {
  test(`saved desktop light appearance wins over stale or missing page cache (${cached})`, () => {
    const { context, root } = appearanceContext(cached, true, false, 'light')
    vm.runInContext(bootstrap, context)
    assert.equal(root.dataset.theme, 'light')
    vm.runInContext(themeCode, context)
    assert.equal(root.dataset.theme, 'light')
    assert.equal(context.exports.theme.value, 'light')
  })
}

const themeSource = readFileSync(new URL('../src/theme.ts', import.meta.url), 'utf8')
const themeCode = ts.transpileModule(themeSource, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText

test('native background follows explicit and changing system appearance', () => {
  const { context, root, backgrounds, media } = appearanceContext('system', false)
  vm.runInContext(themeCode, context)
  assert.equal(root.dataset.theme, 'light')
  assert.deepEqual(backgrounds.at(-1), [244, 246, 250, 255])
  media.matches = true
  media.changed()
  assert.equal(root.dataset.theme, 'dark')
  assert.deepEqual(backgrounds.at(-1), [12, 14, 20, 255])
  context.exports.theme.value = 'light'
  assert.equal(root.dataset.theme, 'light')
  assert.deepEqual(backgrounds.at(-1), [244, 246, 250, 255])
})

const app = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')
const script = app.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true)
const renderedTemplate = compileTemplate({
  source: app.match(/<template>([\s\S]*?)<\/template>\s*<style/)[1],
  filename: 'App.vue', id: 'startup-screen', ssr: true,
  ssrCssVars: [], transformAssetUrls: false,
})
assert.deepEqual(renderedTemplate.errors, [])
const renderContext = vm.createContext({ exports: {}, require: vueRequire })
vm.runInContext(ts.transpileModule(renderedTemplate.code, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText, renderContext)
const components = Object.fromEntries(ast.statements
  .filter(node => ts.isImportDeclaration(node) && node.moduleSpecifier.text.endsWith('.vue'))
  .map(node => [node.importClause.name.text, { render: () => null }]))
components.AlertTriangle = { render: () => null }
components.X = { render: () => null }

test('the actual pending-license template renders no card, message, or spinner', async () => {
  const html = await renderToString(createSSRApp({
    components, ssrRender: renderContext.exports.ssrRender,
    data: () => ({ isLicensed: false, licenseChecking: true }),
  }))
  assert.doesNotMatch(html, /<form|license-screen|license-card|license-loader|正在检查授权|正在读取本机授权/)
})

test('the actual activation form appears after an unlicensed check has completed', async () => {
  const html = await renderToString(createSSRApp({
    components, ssrRender: renderContext.exports.ssrRender,
    data: () => ({
      isLicensed: false, licenseChecking: false, licenseKey: '',
      licenseMessage: '请输入激活码', isActivating: false, activationCooldown: 0,
    }),
  }))
  assert.match(html, /激活专业版/)
  assert.match(html, /license-card/)
  assert.match(html, /请输入激活码/)
  assert.doesNotMatch(html, /正在检查授权|正在读取本机授权|license-loader/)
})

const mounted = ast.statements.find(node => ts.isExpressionStatement(node)
  && ts.isCallExpression(node.expression) && node.expression.expression.getText(ast) === 'onMounted')
const mountedCode = ts.transpileModule(mounted.getText(ast), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText
const licenseFunction = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name.text === 'checkLicense')
const licenseCode = ts.transpileModule(licenseFunction.getText(ast), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText

for (const settingsFail of [false, true]) {
  test(`hidden window waits for appearance and local license, independently of account requests (settings fail=${settingsFail})`, async () => {
    let onMounted, finishSettings, finishLicense
    const events = [], order = []
    const context = vm.createContext({
      onMounted: callback => { onMounted = callback },
      EventsOn: () => () => {}, receiveUpdateState() {}, updateState: { value: {} },
      GetUpdateState: () => new Promise(() => {}),
      loadSettings: () => new Promise((resolve, reject) => {
        finishSettings = () => { order.push('appearance'); settingsFail ? reject(Error('settings unavailable')) : resolve() }
      }),
      loadCachedUserInfo: async () => {},
      nextTick: async () => { order.push('vue update') },
      requestAnimationFrame: () => { throw Error('hidden WebView cannot advance animation frames') },
      EventsEmit: name => { order.push('show'); events.push(name) },
      CheckLicense: () => new Promise(resolve => { finishLicense = resolve }),
      licenseChecking: ref(true), isLicensed: ref(false), licenseStatus: ref(null), licenseMessage: ref(''),
      setLicenseMessage() {},
      initializeAuthorizedApp: () => { order.push('account request'); return new Promise(() => {}) },
      console: { error() {} },
    })
    vm.runInContext(licenseCode + '\n' + mountedCode, context)
    void onMounted()
    assert.deepEqual(events, [])
    finishSettings()
    // Drain the promises for GetSettings and Vue's nextTick.
    await new Promise(resolve => setImmediate(resolve))
    assert.deepEqual(events, [])
    finishLicense({ success: true })
    await new Promise(resolve => setImmediate(resolve))
    assert.deepEqual(events, ['app:ready'])
    assert.equal(context.isLicensed.value, true)
    assert.equal(context.licenseChecking.value, false)
    assert.deepEqual(order, ['appearance', 'vue update', 'show', 'account request'])
  })
}

for (const failed of [false, true]) {
  test(`unlicensed or failed local check shows activation instead of remaining hidden (failure=${failed})`, async () => {
    const events = []
    const context = vm.createContext({
      licenseChecking: ref(true), isLicensed: ref(false), licenseStatus: ref(null), licenseMessage: ref(''),
      CheckLicense: async () => { if (failed) throw Error('local read failed'); return { success: false, message: 'activate' } },
      setLicenseMessage() {}, nextTick: async () => {}, EventsEmit: name => events.push(name),
      initializeAuthorizedApp: () => { throw Error('must not initialize unlicensed app') },
    })
    vm.runInContext(licenseCode, context)
    await context.checkLicense()
    assert.equal(context.licenseChecking.value, false)
    assert.equal(context.isLicensed.value, false)
    assert.deepEqual(events, ['app:ready'])
  })
}
