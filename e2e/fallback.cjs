// A browser that can't decode HEVC (Playwright's Chromium) is told so, the host
// sees who is affected and builds the compatible copy, and the guest switches to it.
'use strict';
const { chromium } = require('playwright');
const fs = require('node:fs');
const path = require('node:path');
const { HEVC, tmpDir, makeVideo, start, joinAsGuest, enterAsHost, trackErrors } = require('./lib.cjs');

(async () => {
  const dir = tmpDir('fallback');
  const srv = await start(dir, makeVideo(path.join(dir, 'Clip.2021.mkv'), { video: HEVC }));
  const browser = await chromium.launch();
  try {
    const guest = await browser.newPage();
    const host = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    const errors = trackErrors({ guest, host });

    await joinAsGuest(guest, srv.link, 'Ana');
    await enterAsHost(host);
    await host.waitForSelector("text=Ana and Host can't play this version", { timeout: 20000 });

    await host.getByRole('button', { name: /Make .* copy/ }).click();
    await guest.waitForFunction(() => {
      const v = document.querySelector('video');
      return v && v.currentSrc.endsWith('/media/compat') && v.videoWidth > 0;
    }, null, { timeout: 120000 });
    await host.waitForFunction(() => !document.querySelector('aside').innerText.includes("can't play"), null, { timeout: 10000 });

    if (errors.length) throw new Error('page errors: ' + errors.join('; '));
    console.log('FALLBACK PASS');
  } catch (e) {
    console.error(e.message + '\n' + srv.log().slice(-1500));
    process.exitCode = 1;
  } finally {
    await browser.close();
    await srv.stop();
    fs.rmSync(dir, { recursive: true, force: true });
  }
})();
