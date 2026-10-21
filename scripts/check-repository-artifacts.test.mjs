import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { classify, check } from './check-repository-artifacts.mjs';

const policy = { maxBytes: 1024, warnBytes: 512, exceptions: [] };
test('content, path and hash jointly enforce fixture exceptions', () => {
  const bytes = Buffer.from('SQLite\0fixture');
  const allowed = { ...policy, exceptions: [{ path: 'fixture.sqlite', kind: 'binary-fixture', sha256: createHash('sha256').update(bytes).digest('hex') }] };
  assert.deepEqual(classify('fixture.sqlite', bytes, allowed), {});
  assert.ok(classify('innocent.txt', bytes, allowed).error);
  assert.ok(classify('fixture.sqlite', Buffer.concat([bytes, Buffer.from('changed')]), allowed).error);
  assert.ok(classify('a.svg', Buffer.from('<svg><image href="data:image/png;base64,x"/></svg>'), policy).error);
  assert.ok(classify('a.json', Buffer.alloc(2048, 65), policy).error);
});

test('product icon exceptions are limited to the exact runtime assets', () => {
  const bytes = Buffer.from([0, 0, 1, 0, 1, 0]);
  for (const iconPath of ['internal/branding/logo.ico', 'internal/branding/logo-light.ico', 'internal/branding/logo_windows_amd64.syso']) {
    const exception = { path: iconPath, kind: 'product-icon', bytes: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex') };
    const allowed = { ...policy, exceptions: [exception] };
    assert.deepEqual(classify(iconPath, bytes, allowed), {});
    assert.ok(classify('docs/evidence/logo.ico', bytes, allowed).error);
    assert.ok(classify(iconPath, Buffer.concat([bytes, Buffer.from('changed')]), allowed).error);
    assert.ok(classify(iconPath, bytes, { ...allowed, exceptions: [{ ...exception, bytes: 999 }] }).error);
    assert.ok(classify('application.exe', bytes, { ...allowed, exceptions: [{ ...exception, path: 'application.exe' }] }).error);
    assert.ok(classify('internal/branding/other.ico', bytes, { ...allowed, exceptions: [{ ...exception, path: 'internal/branding/other.ico' }] }).error);
    assert.ok(classify('internal/branding/other.syso', bytes, { ...allowed, exceptions: [{ ...exception, path: 'internal/branding/other.syso' }] }).error);
  }
});

test('rejects a binary introduced then deleted before HEAD', () => {
  const cwd = mkdtempSync(path.join(os.tmpdir(), 'swd-artifact-policy-'));
  const git = (...args) => execFileSync('git', args, { cwd, windowsHide: true }).toString().trim();
  try {
    git('init', '-q'); git('config', 'user.name', 'Fixture'); git('config', 'user.email', 'fixture@example.invalid');
    writeFileSync(path.join(cwd, 'source.txt'), 'source\n'); git('add', '.'); git('commit', '-qm', 'base');
    const base = git('rev-parse', 'HEAD');
    writeFileSync(path.join(cwd, 'trace.txt'), Buffer.from([0, 1, 2])); git('add', '.'); git('commit', '-qm', 'trace');
    git('rm', '-q', 'trace.txt'); git('commit', '-qm', 'remove trace');
    const result = check({ cwd, policy, base });
    assert.equal(result.errors.length, 1); assert.equal(result.errors[0].path, 'trace.txt');
  } finally { rmSync(cwd, { recursive: true, force: true }); }
});
