import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from 'typescript'

const source = readFileSync(new URL('../src/utils/episode-selection.ts', import.meta.url), 'utf8')
const js = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } }).outputText
const { filterEpisodes, selectEpisodes, episodeURL } = await import(`data:text/javascript;base64,${Buffer.from(js).toString('base64')}`)
const episodes = [
  { index: 8, cid: 108, title: '美的历程 | 美学' },
  { index: 14, cid: 114, title: '菊花与刀 上', longTitle: 'Anthropology' },
  { index: 15, cid: 115, title: '菊花与刀 下' },
  { index: 18, cid: 118, title: '其他书目' },
]

test('keywords and long titles filter immediately without changing original numbering', () => {
  assert.deepEqual(filterEpisodes(episodes, ' 菊花与刀 ').map(ep => ep.index), [14, 15])
  assert.deepEqual(filterEpisodes(episodes, 'ANTHROPOLOGY').map(ep => ep.index), [14])
  assert.deepEqual(filterEpisodes(episodes, '美学').map(ep => ep.index), [8])
  assert.deepEqual(filterEpisodes(episodes, '不存在'), [])
  assert.deepEqual(filterEpisodes(episodes, ''), episodes)
})

test('part numbers match exactly, including P8 and 第8集, rather than also matching P18', () => {
  for (const query of ['P8', '8', '第8集']) assert.deepEqual(filterEpisodes(episodes, query).map(ep => ep.cid), [108])
})

test('selecting and inverting search results preserves choices from other searches', () => {
  let selected = selectEpisodes({}, filterEpisodes(episodes, '美学'))
  selected = selectEpisodes(selected, filterEpisodes(episodes, '菊花与刀'))
  assert.deepEqual(Object.keys(selected).filter(cid => selected[cid]).map(Number), [108, 114, 115])
  selected = selectEpisodes(selected, filterEpisodes(episodes, '菊花与刀'), true)
  assert.equal(selected[108], true)
  assert.equal(selected[114], false)
  assert.equal(selected[115], false)
  assert.equal(selected[118], undefined)
})

test('video-page links use the actual part number rather than the collection index', () => {
  assert.equal(episodeURL({ bvid: 'BVcurrent', index: 23, page: 1 }, 'normal'), 'https://www.bilibili.com/video/BVcurrent/')
  assert.equal(episodeURL({ bvid: 'BVparts', page: 8 }, 'normal'), 'https://www.bilibili.com/video/BVparts/?p=8')
  assert.equal(episodeURL({ epid: 42 }, 'bangumi'), 'https://www.bilibili.com/bangumi/play/ep42')
  assert.equal(episodeURL({ epid: 43 }, 'cheese'), 'https://www.bilibili.com/cheese/play/ep43')
})
