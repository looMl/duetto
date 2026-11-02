// The host uploads an .srt: every viewer is told, can select it and sees the
// cue on screen, bad files get a clear error, and the upload survives a restart.
'use strict';
const { chromium } = require('playwright');
const fs = require('node:fs');
const path = require('node:path');
const { tmpDir, makeVideo, start, waitFor, joinAsGuest, enterAsHost, trackErrors } = require('./lib.cjs');

(async () => {
  const dir = tmpDir('subs');
  const src = makeVideo(path.join(dir, 'Film.2020.mkv'), { seconds: 30 });
  // Latin-1 bytes on purpose: "Perché" must come out right. Not named like a
  // sidecar of the video, so the track can only come from the upload.
  const upload = path.join(dir, 'upload', 'Shining ITA.srt');
  fs.mkdirSync(path.dirname(upload));
  fs.writeFileSync(upload, '1\r\n00:00:00,000 --> 00:00:29,000\r\nPerch\xe9 no?\r\n', 'latin1');

  let srv = await start(dir, src);
  const browser = await chromium.launch({ args: ['--autoplay-policy=no-user-gesture-required'] });
  try {
    const guest = await browser.newPage();
    const host = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    const errors = trackErrors({ guest, host });

    await joinAsGuest(guest, srv.link, 'Ana');
    await guest.waitForFunction(() => (document.querySelector('video')?.readyState ?? 0) >= 3, null, { timeout: 20000 });
    await enterAsHost(host);

    await host.setInputFiles('aside input[type=file]', upload);
    await guest.waitForSelector('text=Subtitles added: Italiano', { timeout: 5000 });
    await host.waitForSelector('aside >> text=Italiano');

    await guest.mouse.move(300, 300);
    await guest.getByRole('button', { name: 'Subtitles' }).click();
    await guest.getByRole('menuitemradio', { name: 'Italiano' }).click();
    await guest.keyboard.press('Space'); // cues are only evaluated while time advances
    await guest.waitForFunction(() => {
      const t = Array.from(document.querySelector('video').textTracks).find((x) => x.mode === 'showing');
      return t?.activeCues?.[0]?.text === 'Perché no?';
    }, null, { timeout: 5000 });

    const bad = path.join(dir, 'upload', 'bad.srt');
    fs.writeFileSync(bad, 'not subtitles');
    await host.setInputFiles('aside input[type=file]', bad);
    await host.waitForSelector('text=no subtitles found in this file');

    await srv.stop();
    srv = await start(dir, src);
    await waitFor(() => /prepared/.test(srv.log()), 20000);
    const late = await browser.newPage();
    await joinAsGuest(late, srv.link, 'Bea');
    await late.waitForFunction(() => Array.from(document.querySelectorAll('track')).some((t) => t.label === 'Italiano'), null, { timeout: 10000 });

    if (errors.length) throw new Error('page errors: ' + errors.join('; '));
    console.log('SUBTITLES PASS');
  } catch (e) {
    console.error(e.message + '\n' + srv.log().slice(-1500));
    process.exitCode = 1;
  } finally {
    await browser.close();
    await srv.stop();
    fs.rmSync(dir, { recursive: true, force: true });
  }
})();
