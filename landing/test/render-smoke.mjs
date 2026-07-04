#!/usr/bin/env node
// Rendered smoke check (fix-plan item 3, landing-testing-20260702-181558.md).
//
// Builds the Vite "preview" bundle (the same SSR entry `npm run preview`
// serves — see src/entry.preview.tsx) and asserts `/` and the docs index
// render without error and contain a handful of stable anchors. This is a
// canary, not a visual regression suite; deeper content-accuracy auditing
// stays with the check-landing skill.
//
// Deliberately not Playwright: a plain HTTP fetch against `vite preview` is
// enough to prove SSR doesn't throw and the expected markup is present,
// without paying Playwright's ~1-2 min browser-install cost in CI for every
// landing/** change. Revisit if a future assertion needs real DOM/JS
// execution (e.g. client-side hydration behavior).

import { spawn } from 'node:child_process';
import process from 'node:process';

const PORT = 4173 + Math.floor(Math.random() * 1000); // avoid clashing with a stray local server
const BASE = `http://localhost:${PORT}`;
const START_TIMEOUT_MS = 30_000;

function run(cmd, args) {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, { stdio: 'inherit' });
    child.on('exit', (code) => {
      if (code === 0) resolve();
      else reject(new Error(`${cmd} ${args.join(' ')} exited with code ${code}`));
    });
    child.on('error', reject);
  });
}

async function waitForServer(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let lastErr;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url);
      if (res.ok || res.status < 500) return;
    } catch (err) {
      lastErr = err;
    }
    await new Promise((r) => setTimeout(r, 300));
  }
  throw new Error(`Server did not become ready at ${url}: ${lastErr}`);
}

async function assertPageRenders(path, mustContain) {
  const url = `${BASE}${path}`;
  const res = await fetch(url, { redirect: 'follow' });
  if (!res.ok) {
    throw new Error(`GET ${url} returned ${res.status}`);
  }
  const body = await res.text();
  for (const needle of mustContain) {
    if (!body.includes(needle)) {
      throw new Error(`GET ${url} response missing expected content: ${JSON.stringify(needle)}`);
    }
  }
  console.log(`ok  ${path}  (${res.status}, ${body.length} bytes, contains ${mustContain.length} expected anchors)`);
}

async function main() {
  console.log('--- building preview bundle (build.client + build.preview) ---');
  await run('npx', ['vite', 'build']);
  await run('npx', ['vite', 'build', '--ssr', 'src/entry.preview.tsx']);

  console.log(`--- starting preview server on port ${PORT} ---`);
  const server = spawn('npx', ['vite', 'preview', '--port', String(PORT), '--strictPort'], {
    stdio: 'inherit',
  });

  let exitCode = 0;
  try {
    await waitForServer(BASE, START_TIMEOUT_MS);

    await assertPageRenders('/', ['Kaptanto', 'get.kaptan.to']);
    await assertPageRenders('/docs/', ['Documentation index', '/docs/docs-intro']);
    await assertPageRenders('/docs/docs-intro', ['Kaptanto Docs', 'docs-intro']);

    console.log('--- rendered smoke check passed ---');
  } catch (err) {
    console.error('--- rendered smoke check FAILED ---');
    console.error(err);
    exitCode = 1;
  } finally {
    server.kill('SIGTERM');
  }

  process.exit(exitCode);
}

main();
