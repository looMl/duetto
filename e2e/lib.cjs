// Shared helpers for the end-to-end scripts: node e2e/<name>.cjs <duetto-binary>
'use strict';
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const BIN = path.resolve(process.argv[2] || 'duetto');
const ADMIN = 17790;
const PUBLIC = 17791;
const HOST_URL = `http://127.0.0.1:${ADMIN}/`;

const tmpDir = (prefix) => fs.mkdtempSync(path.join(os.tmpdir(), `duetto-${prefix}-`));

/** Synthesizes a test video with ffmpeg. */
function makeVideo(file, { size = '640x360', seconds = 20, video = ['-c:v', 'libx264', '-preset', 'ultrafast'], audio = 'aac' } = {}) {
  execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'lavfi', '-i', `testsrc2=s=${size}:r=24:d=${seconds}`,
    '-f', 'lavfi', '-i', `sine=d=${seconds}`, ...video, '-c:a', audio, file]);
  return file;
}

const HEVC = ['-c:v', 'libx265', '-preset', 'ultrafast', '-x265-params', 'log-level=none'];

/** Starts duetto without tunnel or browser; resolves once the guest link is printed. */
async function start(dir, file, ...extra) {
  const proc = spawn(BIN, ['-no-tunnel', '-no-browser', '-file', file, '-cache', path.join(dir, 'cache'),
    '-admin-port', String(ADMIN), '-public-port', String(PUBLIC), '-library', dir, ...extra]);
  let out = '';
  proc.stdout.on('data', (d) => (out += d));
  proc.stderr.on('data', (d) => (out += d));
  const log = () => out;
  await waitFor(() => /Guest link/.test(out), 10000).catch(() => {
    proc.kill();
    throw new Error('duetto did not start:\n' + out);
  });
  return { proc, log, link: out.match(/Guest link \(local\): (\S+)/)[1], stop: () => new Promise((r) => { proc.on('exit', r); proc.kill(); }) };
}

async function waitFor(fn, ms) {
  for (const end = Date.now() + ms; Date.now() < end; await new Promise((r) => setTimeout(r, 100))) if (fn()) return;
  throw new Error('timeout');
}

async function joinAsGuest(page, link, name) {
  await page.goto(link);
  await page.getByPlaceholder('Your name').fill(name);
  await page.getByRole('button', { name: 'Join' }).click();
}

/** Enters the host page and makes sure the host panel is open. */
async function enterAsHost(page) {
  await page.goto(HOST_URL);
  await page.getByRole('button', { name: 'Enter' }).click();
  await page.waitForTimeout(1500); // the panel opens by itself only while no movie is loaded
  if (!(await page.isVisible('aside'))) await page.getByRole('button', { name: 'Invite and settings' }).click();
}

/** Collects uncaught page errors; the scripts fail if any appear. */
function trackErrors(pages) {
  const errors = [];
  for (const [name, p] of Object.entries(pages)) p.on('pageerror', (e) => errors.push(`${name}: ${e.message}`));
  return errors;
}

module.exports = { BIN, ADMIN, PUBLIC, HOST_URL, HEVC, tmpDir, makeVideo, start, waitFor, joinAsGuest, enterAsHost, trackErrors };
