// Two real browsers (host + guest) against a real duetto binary: join flow,
// lossless remux playback, drift, shared pause/seek, leaving, auth.
'use strict';
const { chromium } = require('playwright');
const fs = require('node:fs');
const path = require('node:path');
const { ADMIN, PUBLIC, tmpDir, makeVideo, start } = require('./lib.cjs');

function assert(cond, msg) {
  if (!cond) throw new Error('ASSERTION FAILED: ' + msg);
}

/** Records (wall ms, currentTime) every 50 ms inside the page. */
const RECORDER = () => {
  window.__samples = [];
  setInterval(() => {
    const v = document.querySelector('video');
    if (v && !v.paused && !v.seeking) window.__samples.push([performance.timeOrigin + performance.now(), v.currentTime]);
  }, 50);
};

function interpolate(samples, t) {
  for (let i = 1; i < samples.length; i++) {
    const [t0, p0] = samples[i - 1];
    const [t1, p1] = samples[i];
    if (t0 <= t && t <= t1 && t1 - t0 < 200) return p0 + ((p1 - p0) * (t - t0)) / (t1 - t0);
  }
  return null;
}

async function main() {
  const dir = tmpDir('sync');
  const file = makeVideo(path.join(dir, 'Test.Movie.2024.mkv'), {
    size: '1280x720', seconds: 180, video: ['-c:v', 'libx264', '-preset', 'ultrafast', '-g', '48'], audio: 'ac3',
  });
  fs.writeFileSync(path.join(dir, 'Test.Movie.2024.srt'), '1\n00:00:01,000 --> 00:00:05,000\nHello\n');
  const srv = await start(dir, file, '-name', 'Luca');
  const browser = await chromium.launch({ args: ['--autoplay-policy=no-user-gesture-required'] });
  const results = {};
  try {
    const host = await (await browser.newContext()).newPage();
    const guest = await (await browser.newContext()).newPage();
    const errors = [];
    for (const [n, p] of [['host', host], ['guest', guest]]) {
      p.on('pageerror', (e) => errors.push(`${n}: ${e.message}`));
      p.on('console', (m) => m.type() === 'error' && errors.push(`${n} console: ${m.text()}`));
    }

    // Host: loopback admin page, one click to satisfy autoplay policy.
    await host.goto(`http://127.0.0.1:${ADMIN}/`);
    await host.getByRole('button', { name: 'Enter' }).click();

    // Guest: invite link → name → Join.
    await guest.goto(srv.link);
    await guest.getByPlaceholder('Your name').fill('Ana');
    await guest.getByRole('button', { name: 'Join' }).click();
    assert(!(await guest.evaluate(() => location.hash)), 'invite key must be removed from the URL');

    for (const p of [host, guest]) {
      await p.waitForFunction(() => (document.querySelector('video')?.readyState ?? 0) >= 3, null, { timeout: 30000 });
      await p.evaluate(RECORDER);
    }
    await host.getByRole('button', { name: 'Close' }).click().catch(() => {}); // host panel opens if no media yet
    await guest.waitForSelector('text=Luca'); // guest sees host chip
    results.subtitles = await guest.evaluate(() => document.querySelector('track')?.getAttribute('label'));

    // --- R3: drift while playing ---
    await guest.getByRole('button', { name: 'Play' }).first().click();
    await host.waitForFunction(() => !document.querySelector('video').paused, null, { timeout: 5000 });
    await host.waitForTimeout(25000);
    const [a, b] = await Promise.all([host, guest].map((p) => p.evaluate(() => window.__samples)));
    const settled = a.filter(([t]) => t > a[0][0] + 5000); // allow 5 s convergence
    const drifts = settled.map(([t, p]) => { const q = interpolate(b, t); return q === null ? null : Math.abs(p - q); })
      .filter((d) => d !== null).sort((x, y) => x - y);
    results.driftSamples = drifts.length;
    results.driftP50ms = Math.round(drifts[Math.floor(drifts.length * 0.5)] * 1000);
    results.driftP95ms = Math.round(drifts[Math.floor(drifts.length * 0.95)] * 1000);
    results.driftMaxms = Math.round(drifts[drifts.length - 1] * 1000);
    assert(drifts.length > 100, 'not enough overlapping samples');
    assert(results.driftP95ms <= 50, `p95 drift ${results.driftP95ms} ms > 50 ms`);

    // --- R4: pause from guest propagates to host ---
    await guest.keyboard.press('Space');
    const t0 = Date.now();
    await host.waitForFunction(() => document.querySelector('video').paused, null, { timeout: 2000 });
    results.pausePropagationMs = Date.now() - t0;
    await host.waitForTimeout(500);
    const [ph, pg] = await Promise.all([host, guest].map((p) => p.evaluate(() => document.querySelector('video').currentTime)));
    results.pausedPositionDiffMs = Math.round(Math.abs(ph - pg) * 1000);
    assert(results.pausedPositionDiffMs < 100, 'paused positions differ');
    assert(await host.isVisible('text=Ana paused'), 'host should see a toast');

    // --- R4: seek from host propagates to guest ---
    await host.evaluate(() => document.querySelector('.seek').focus());
    for (let i = 0; i < 6; i++) await host.keyboard.press('ArrowRight'); // +60 s
    await guest.waitForFunction((x) => Math.abs(document.querySelector('video').currentTime - x) < 0.2, ph + 60, { timeout: 5000 });
    results.seekOk = true;

    // --- resume, then guest leaves: host keeps playing ---
    await host.keyboard.press('Space');
    await guest.waitForFunction(() => !document.querySelector('video').paused, null, { timeout: 5000 });
    await guest.close();
    await host.waitForTimeout(2000);
    assert(!(await host.evaluate(() => document.querySelector('video').paused)), 'host must keep playing after guest leaves');

    // --- security: guest-facing port refuses media without a session ---
    const res = await fetch(`http://127.0.0.1:${PUBLIC}/media/original`);
    results.mediaWithoutSession = res.status;
    assert(res.status === 401, 'media must require a session');

    results.pageErrors = errors;
    assert(errors.length === 0, 'page errors: ' + errors.join('; '));
    console.log(JSON.stringify(results, null, 2));
    console.log('E2E PASS');
  } catch (e) {
    console.log(JSON.stringify(results, null, 2));
    console.error(e.message);
    console.error('--- server log ---\n' + srv.log().slice(-3000));
    process.exitCode = 1;
  } finally {
    await browser.close();
    await srv.stop();
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

main();
