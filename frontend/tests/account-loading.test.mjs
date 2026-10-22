import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import ts from 'typescript'
import { createSSRApp, h, ref } from 'vue'

const app = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')
const script = app.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true)
const names = new Set(['loadCachedUserInfo', 'refreshUserInfo', 'handleLoginSuccess', 'handleLogoutSuccess'])
const code = ts.transpileModule(ast.statements
  .filter(n => ts.isFunctionDeclaration(n) && names.has(n.name.text))
  .map(n => n.getText(ast)).join('\n'), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
const profile = { isLogin: true, uname: 'Saved Account', face: 'https://example.test/avatar.png', vipStatus: 1, vipLabel: '月度大会员' }

function harness(cached) {
  const requests = []
  const context = vm.createContext({
    userInfo: ref(null), userInfoLoading: ref(true), userInfoError: ref(false), userInfoRequest: 0,
    GetCachedUserInfo: async () => cached,
    GetUserInfo: () => new Promise((resolve, reject) => requests.push({ resolve, reject })),
    showToast() {}, quickParseRef: ref(null), console: { error() {} },
  })
  vm.runInContext(code, context)
  return { c: context, requests }
}

test('a cached account is available before any remote request and stays visible while refreshing', async () => {
  const { c, requests } = harness(profile)
  await c.loadCachedUserInfo()
  assert.equal(requests.length, 0)
  assert.equal(c.userInfo.value.uname, profile.uname)
  assert.equal(c.userInfoLoading.value, false)
  const pending = c.refreshUserInfo()
  assert.equal(c.userInfoLoading.value, false)
  requests[0].resolve({ ...profile, uname: 'Updated Account' })
  await pending
  assert.equal(c.userInfo.value.uname, 'Updated Account')
})

test('stored credentials with no profile remain loading until the response confirms the account', async () => {
  const { c, requests } = harness(null)
  await c.loadCachedUserInfo()
  const pending = c.refreshUserInfo()
  assert.equal(c.userInfoLoading.value, true)
  assert.equal(c.userInfoError.value, false)
  requests[0].resolve(profile)
  await pending
  assert.equal(c.userInfoLoading.value, false)
  assert.equal(c.userInfo.value.uname, profile.uname)
})

test('no local credentials are a known logged-out state without waiting for the network', async () => {
  const { c } = harness({ isLogin: false })
  await c.loadCachedUserInfo()
  assert.equal(c.userInfoLoading.value, false)
  assert.equal(c.userInfo.value.isLogin, false)
})

for (const cached of [profile, null]) {
  test(`a failed request preserves a cached account or offers retry (cache=${!!cached})`, async () => {
    const { c, requests } = harness(cached)
    await c.loadCachedUserInfo()
    const pending = c.refreshUserInfo()
    requests[0].reject(Error('offline'))
    await pending
    assert.equal(c.userInfoLoading.value, false)
    assert.equal(c.userInfoError.value, !cached)
    if (cached) assert.equal(c.userInfo.value.uname, profile.uname)
  })
}

test('late startup results cannot undo logout or replace a newly logged-in account', async () => {
  for (const switchAccount of [false, true]) {
    const { c, requests } = harness(profile)
    await c.loadCachedUserInfo()
    const pending = c.refreshUserInfo()
    if (switchAccount) c.handleLoginSuccess({ ...profile, uname: 'New Account' })
    else c.handleLogoutSuccess()
    requests[0].resolve(profile)
    await pending
    assert.equal(c.userInfo.value?.uname, switchAccount ? 'New Account' : undefined)
    assert.equal(c.userInfoLoading.value, false)
    assert.equal(c.userInfoError.value, false)
  }
})

test('a confirmed expired login replaces a cached account with the logged-out state', async () => {
  const { c, requests } = harness(profile)
  await c.loadCachedUserInfo()
  const pending = c.refreshUserInfo()
  requests[0].resolve({ isLogin: false })
  await pending
  assert.equal(c.userInfo.value.isLogin, false)
  assert.equal(c.userInfoError.value, false)
})

// Render the real sidebar so the test checks visible words, not just refs.
const vueRequire = createRequire(import.meta.resolve('vue/package.json'))
const { parse, compileTemplate } = vueRequire('@vue/compiler-sfc')
const { renderToString } = vueRequire('@vue/server-renderer')
const sidebar = parse(readFileSync(new URL('../src/components/Sidebar.vue', import.meta.url), 'utf8')).descriptor
const compiled = compileTemplate({ source: sidebar.template.content, filename: 'Sidebar.vue', id: 'account', ssr: true, ssrCssVars: [], transformAssetUrls: false })
assert.deepEqual(compiled.errors, [])
const renderContext = vm.createContext({ exports: {}, require: vueRequire })
vm.runInContext(ts.transpileModule(compiled.code, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText, renderContext)
const iconNames = ['Sparkles', 'Download', 'CheckCircle2', 'SettingsIcon', 'UserIcon', 'Crown', 'Tv', 'LoaderCircle']
const components = Object.fromEntries(iconNames.map(name => [name, { render() { return h('svg', this.$attrs) } }]))

for (const [name, state, expected] of [
  ['cached', { userInfo: profile }, 'Saved Account'],
  ['loading', { userInfoLoading: true }, '正在加载账号'],
  ['error', { userInfoError: true }, '点击重试'],
  ['logged out', {}, '点击登录账号'],
]) {
  test(`sidebar displays the correct ${name} account state`, async () => {
    const html = await renderToString(createSSRApp({
      components, ssrRender: renderContext.exports.ssrRender,
      data: () => ({ activeTab: 'parse', activeTaskCount: 0, completedTaskCount: 0, hasUpdate: false,
        userInfo: null, userInfoLoading: false, userInfoError: false, isVip: false, ...state }),
      methods: { normalizeImg: url => url },
    }))
    assert.ok(html.includes(expected))
    if (name !== 'logged out') assert.ok(!html.includes('点击登录账号'))
    assert.equal(html.includes('account-spinner'), name === 'loading')
  })
}
