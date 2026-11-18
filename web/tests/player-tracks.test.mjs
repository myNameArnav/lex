import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';
import * as substyle from '../static/js/substyle.js';

// Loads static/js/player/tracks.js with its app-wide imports stubbed.
async function loadTracks({ me = null, prefValues = {} } = {}) {
  const context = vm.createContext({});
  const deps = {
    '../app.js': { state: { me } },
    '../prefs.js': { prefs: { all: () => ({ ...substyle.SUB_STYLE_DEFAULTS, ...prefValues }) } },
    '../substyle.js': { ...substyle },
  };
  const source = await readFile(new URL('../static/js/player/tracks.js', import.meta.url), 'utf8');
  const module = new vm.SourceTextModule(source, { context });
  await module.link((specifier) => {
    const values = deps[specifier];
    assert.ok(values, `tracks.js imports ${specifier}: stub it`);
    return new vm.SyntheticModule(Object.keys(values), function () {
      for (const [name, value] of Object.entries(values)) this.setExport(name, value);
    }, { context });
  });
  await module.evaluate();
  return module.namespace;
}

const file = (audio, subtitles) => ({ info: { streams: audio.map((a) => ({ type: 'audio', ...a })) }, subtitles });

test('pickAudio prefers the asked-for language, then the default track, then the first', async () => {
  const { pickAudio } = await loadTracks();
  const f = file([{ language: 'jpn' }, { language: 'eng', default: true }, { language: 'fre' }]);
  assert.equal(pickAudio(f, 'fre').language, 'fre');
  assert.equal(pickAudio(f, 'ger').language, 'eng');
  assert.equal(pickAudio(file([{ language: 'jpn' }, { language: 'eng' }]), '').language, 'jpn');
  assert.equal(pickAudio(file([]), 'eng'), -1);
});

test('pickSubtitle: off, always, and auto (forced subs, or full subs for foreign audio)', async () => {
  const { pickSubtitle } = await loadTracks();
  const subs = [
    { index: 3, language: 'eng', forced: true, textSub: true },
    { index: 4, language: 'eng', textSub: false },
    { index: 5, language: 'eng', textSub: true },
    { index: 6, language: 'spa', textSub: true },
  ];
  const eng = { language: 'eng' }, jpn = { language: 'jpn' };
  assert.equal(pickSubtitle(file([], subs), eng, { subMode: 'off', subLang: 'eng' }), -1);
  // always: the full (not forced) text track in the wanted language.
  assert.equal(pickSubtitle(file([], subs), eng, { subMode: 'always', subLang: 'eng' }), 5);
  assert.equal(pickSubtitle(file([], subs), eng, { subMode: 'always', subLang: 'spa' }), 6);
  // auto: forced subs for audio in the viewer's language...
  assert.equal(pickSubtitle(file([], subs), eng, { subMode: 'auto', subLang: 'eng' }), 3);
  // ...and full subs when the audio is in another language.
  assert.equal(pickSubtitle(file([], subs.slice(1)), jpn, { subMode: 'auto', subLang: 'eng' }), 5);
  assert.equal(pickSubtitle(file([], subs.slice(1)), eng, { subMode: 'auto', subLang: 'eng' }), -1);
});

test('canRemoveSub: admins, or whoever downloaded the subtitle', async () => {
  assert.equal((await loadTracks()).canRemoveSub({ downloadedBy: 1 }), false);
  assert.equal((await loadTracks({ me: { id: 2 } })).canRemoveSub({ downloadedBy: 1 }), false);
  assert.equal((await loadTracks({ me: { id: 1 } })).canRemoveSub({ downloadedBy: 1 }), true);
  assert.equal((await loadTracks({ me: { id: 2, isAdmin: true } })).canRemoveSub({}), true);
});

test('styleASS restyles bottom dialogue styles and leaves signs alone', async () => {
  const { styleASS } = await loadTracks({ prefValues: { subColor: 'yellow' } });
  const script = [
    '[Script Info]', 'PlayResY: 360', '',
    '[V4+ Styles]',
    'Format: Name, Fontname, Fontsize, PrimaryColour, Alignment',
    'Style: Default,Arial,20,&H00FFFFFF,2',
    'Style: Sign,Arial,20,&H00FFFFFF,2',
    'Style: Top,Arial,20,&H00FFFFFF,8',
    '', '[Events]', 'Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hi',
  ].join('\n');
  const out = styleASS(script).split('\n');
  assert.notEqual(out[5], 'Style: Default,Arial,20,&H00FFFFFF,2');
  assert.equal(out[6], 'Style: Sign,Arial,20,&H00FFFFFF,2');
  assert.equal(out[7], 'Style: Top,Arial,20,&H00FFFFFF,8');
  assert.equal(out.at(-1), 'Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hi');
  assert.equal(styleASS('no events here'), 'no events here');
});
