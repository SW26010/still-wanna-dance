import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { withTemp } from './release-source.mjs';
import { checkValidationReuse, needsProductValidation, productValidationInputs, hasSuccessfulCheck } from './release-validation.mjs';
import webuiManifest from '../webui/build-manifest.json' with { type: 'json' };

function fixture() {
  const passed = name => ({ name, status: 'completed', conclusion: 'success' });
  return {
    repository: 'owner/repo', workflowID: 42, currentRunID: 101,
    run: { id: 100, run_attempt: 1, repository: { full_name: 'owner/repo' }, head_repository: { full_name: 'owner/repo' }, workflow_id: 42, status: 'completed', conclusion: 'success', head_sha: 'a'.repeat(40), event: 'workflow_dispatch', head_branch: 'main' },
    jobs: [
      { ...passed('test'), run_attempt: 1, steps: [passed('Full product checks')] },
      ...Array.from({ length: 6 }, (_, n) => ({ ...passed(`race (${n})`), run_attempt: 1, steps: [passed('Full Windows race shard')] })),
    ],
    files: ['docs/packaging.md', 'scripts/release-notices.mjs', 'scripts/release-notices.test.mjs'],
  };
}

test('delivery-only changes reuse a full successful run, including a full tag run', () => {
  const input = fixture();
  assert.deepEqual(checkValidationReuse(input), { runID: 100, revision: 'a'.repeat(40), changedFiles: input.files });
  input.run.event = 'push'; input.run.head_branch = 'v1.2.0';
  assert.doesNotThrow(() => checkValidationReuse(input));
});

test('product, test, dependency, validation and workflow changes invalidate reuse', () => {
  for (const file of ['internal/console/server.go', 'internal/console/server_test.go', 'webui/pnpm-lock.yaml', 'webui/e2e/example.spec.ts', 'go.mod', 'go.sum', '.gitattributes', 'scripts/go-patches.mjs', 'scripts/release-preflight.ps1', 'scripts/release-validation.mjs', '.github/workflows/check.yml', 'scripts/release.mjs', 'scripts/release-source.mjs', 'scripts/release.test.mjs', 'scripts/release-source.test.mjs', 'scripts/not-a-delivery-script.mjs']) {
    assert.throws(() => checkValidationReuse({ ...fixture(), files: [file] }), /stale/, file);
  }
});

test('partial, failed, foreign and currently running baselines are rejected', () => {
  for (const patch of [{ id: 101 }, { repository: { full_name: 'other/repo' } }, { head_repository: { full_name: 'fork/repo' } }, { workflow_id: 5 }, { status: 'in_progress' }, { conclusion: 'failure' }, { event: 'pull_request' }, { event: 'push', head_branch: 'main' }, { head_sha: 'bad' }]) {
    const input = fixture(); Object.assign(input.run, patch);
    assert.throws(() => checkValidationReuse(input), /completed successful full check/);
  }
});

test('every full product/race job and step must pass; skipped reuse cannot chain', () => {
  for (let n = 0; n < 7; n++) {
    for (const mutation of [
      input => input.jobs.splice(n, 1),
      input => input.jobs.push(input.jobs[n]),
      input => { input.jobs[n].conclusion = 'skipped'; },
      input => { input.jobs[n].steps[0].conclusion = 'skipped'; },
      input => { input.jobs[n].steps[0].name = 'Representative checks'; },
      input => { delete input.jobs[n].run_attempt; },
      input => { input.jobs[n].run_attempt = 2; },
    ]) {
      const input = fixture(); mutation(input);
      assert.throws(() => checkValidationReuse(input), /did not complete/);
    }
  }
});

test('delivery-only retries retain passed checks while each business job uses its latest attempt', () => {
  const deliveryRetry = fixture(); deliveryRetry.run.run_attempt = 2;
  deliveryRetry.jobs.push({ name: 'package', run_attempt: 2, status: 'completed', conclusion: 'success' });
  assert.doesNotThrow(() => checkValidationReuse(deliveryRetry));
  for (let n = 0; n < 7; n++) {
    const retried = fixture(); retried.run.run_attempt = 2;
    const original = retried.jobs[n];
    retried.jobs.push({ ...original, run_attempt: 2, conclusion: 'failure' });
    assert.throws(() => checkValidationReuse(retried), /did not complete/);
    retried.jobs[n] = { ...original, conclusion: 'failure' };
    retried.jobs[7] = { ...original, run_attempt: 2 };
    assert.doesNotThrow(() => checkValidationReuse(retried));
  }
});

test('daily filtering and validation reuse share the documentation and delivery boundary', () => {
  const documentation = ['docs/development.md', 'docs/acceptance/sample.md',
    'docs/evidence/sample/results.json', 'docs/evidence/sample/fixtures/probe.patch',
    'docs/images/webui-library.png', 'scripts/package-portable.ps1'];
  assert.deepEqual(productValidationInputs(documentation), []);
  assert.doesNotThrow(() => checkValidationReuse({ ...fixture(), files: documentation }));
  const compiled = [...new Set([...Object.keys(webuiManifest.inputs), ...Object.keys(webuiManifest.outputs)])];
  const unknown = ['docs/evidence/runtime.go', 'docs/research/unknown.json', 'unknown-file'];
  assert.deepEqual(productValidationInputs([...compiled, ...unknown]), [...compiled, ...unknown]);
  for (const file of [...compiled, ...unknown]) {
    assert.throws(() => checkValidationReuse({ ...fixture(), files: [file] }), /stale/, file);
  }
});

test('daily skips require a completed successful check on the exact comparison revision', () => {
  const run = fixture().run;
  assert.equal(hasSuccessfulCheck([run], 'owner/repo', run.head_sha), true);
  assert.equal(hasSuccessfulCheck([], 'owner/repo', run.head_sha), false);
  for (const patch of [{ head_sha: 'b'.repeat(40) }, { status: 'in_progress' }, { conclusion: 'failure' },
    { conclusion: 'cancelled' }, { repository: { full_name: 'other/repo' } }, { head_repository: { full_name: 'fork/repo' } }]) {
    assert.equal(hasSuccessfulCheck([{ ...run, ...patch }], 'owner/repo', run.head_sha), false);
  }
});

function repository(root) {
  const git = (...args) => execFileSync('git', args, { cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  git('init', '--quiet', '--initial-branch=main');
  git('config', 'core.autocrlf', 'false');
  const commit = files => {
    for (const [file, content] of Object.entries(files)) {
      fs.mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
      fs.writeFileSync(path.join(root, file), content);
    }
    git('add', '-A');
    git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '--quiet', '-m', 'fixture');
    return git('rev-parse', 'HEAD');
  };
  return { git, commit };
}

test('push planning includes every commit and both sides of deletions and renames', () => withTemp(root => {
  const { git, commit } = repository(root);
  const base = commit({ 'internal/example.go': 'package example\n', 'docs/guide.md': 'guide\n' });
  commit({ 'docs/guide.md': 'revised guide\n' });
  const event = { eventName: 'push', ref: 'refs/heads/main', before: base };
  assert.equal(needsProductValidation(event, root), false);
  git('mv', 'docs/guide.md', 'docs/renamed.md'); commit({});
  assert.equal(needsProductValidation(event, root), false);
  commit({ 'internal/example.go': 'package example\n// changed\n' });
  commit({ 'docs/renamed.md': 'another documentation commit\n' });
  assert.equal(needsProductValidation(event, root), true);
  const before = git('rev-parse', 'HEAD');
  git('mv', 'internal/example.go', 'docs/example.md'); commit({});
  assert.equal(needsProductValidation({ ...event, before }, root), true);
}));

test('PR planning uses its merge base, excluding changes made only on the base branch', () => withTemp(root => {
  const { git, commit } = repository(root);
  const base = commit({ 'internal/example.go': 'package example\n' });
  git('checkout', '--quiet', '-b', 'topic');
  const head = commit({ 'docs/guide.md': 'guide\n' });
  git('checkout', '--quiet', 'main');
  const advanced = commit({ 'internal/base.go': 'package example\n' });
  const event = { eventName: 'pull_request', ref: 'refs/pull/1/merge', base: advanced, head };
  assert.notEqual(base, advanced);
  assert.equal(needsProductValidation(event, root), false);
  git('checkout', '--quiet', 'topic');
  commit({ 'internal/topic.go': 'package example\n' });
  const updated = commit({ 'docs/guide.md': 'updated guide\n' });
  assert.equal(needsProductValidation({ ...event, head: updated }, root), true);
}));

test('manual/tag checks and unavailable or invalid comparisons always require product validation', () => withTemp(root => {
  for (const event of [
    { eventName: 'workflow_dispatch' }, { eventName: 'push', ref: 'refs/tags/v2.3.4' },
    { eventName: 'push', ref: 'refs/heads/main', before: '0'.repeat(40) },
    { eventName: 'push', ref: 'refs/heads/main', before: 'a'.repeat(40) },
    { eventName: 'push', ref: 'refs/heads/main', before: '--invalid' },
    { eventName: 'pull_request', base: 'a'.repeat(40) },
    { eventName: 'pull_request', base: 'a'.repeat(40), head: 'b'.repeat(40) },
    { eventName: 'merge_group' },
  ]) assert.equal(needsProductValidation(event, root), true, JSON.stringify(event));
}));

test('the workflow planning step skips documentation pushes/PRs but retains manual checks', () => withTemp(async root => {
  for (const file of ['scripts/release-validation.mjs', 'webui/build-manifest.json']) {
    fs.mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
    fs.copyFileSync(new URL('../' + file, import.meta.url), path.join(root, file));
  }
  const { commit } = repository(root);
  const base = commit({ 'internal/example.go': 'package example\n' });
  const head = commit({ 'docs/evidence/sample/results.json': '{}\n', 'docs/evidence/sample/fixtures/probe.patch': 'archived patch\n' });
  const workflow = fs.readFileSync(new URL('../.github/workflows/check.yml', import.meta.url), 'utf8');
  const lines = workflow.split('          script: |')[1].split(/\r?\n/).slice(1), body = [];
  for (const line of lines) { if (line && !line.startsWith('            ')) break; body.push(line.slice(12)); }
  const plan = new (Object.getPrototypeOf(async function () {}).constructor)('context', 'core', 'github', 'require', body.join('\n'));
  const saved = Object.fromEntries(['GITHUB_WORKSPACE', 'VALIDATION_RUN_ID', 'PREVIOUS_TAG', 'RELEASE_TAG'].map(name => [name, process.env[name]]));
  try {
    Object.assign(process.env, { GITHUB_WORKSPACE: root, VALIDATION_RUN_ID: '', PREVIOUS_TAG: '', RELEASE_TAG: '' });
    for (const [context, expected, prior = 'passed'] of [
      [{ eventName: 'push', ref: 'refs/heads/main', payload: { before: base } }, 'false'],
      [{ eventName: 'pull_request', ref: 'refs/pull/1/merge', payload: { pull_request: { base: { sha: base }, head: { sha: head } } } }, 'false'],
      [{ eventName: 'workflow_dispatch', ref: 'refs/heads/main', payload: {} }, 'true'],
      [{ eventName: 'push', ref: 'refs/heads/main', payload: { before: base } }, 'true', 'missing'],
      [{ eventName: 'push', ref: 'refs/heads/main', payload: { before: base } }, 'true', 'unavailable'],
    ]) {
      const outputs = {};
      const github = { rest: { actions: { listWorkflowRuns() {} } }, paginate: async (_method, params) => {
        assert.equal(params.workflow_id, 'check.yml'); assert.equal(params.head_sha, base);
        if (prior === 'unavailable') throw Error('API unavailable');
        return prior === 'passed' ? [{ ...fixture().run, head_sha: base }] : [];
      } };
      await plan({ ...context, repo: { owner: 'owner', repo: 'repo' } },
        { setOutput: (name, value) => { outputs[name] = value; }, info: () => {}, warning: () => {} }, github, createRequire(import.meta.url));
      assert.equal(outputs.product, expected);
      assert.equal(outputs.reuse, 'false');
    }
  } finally {
    for (const [name, value] of Object.entries(saved)) { if (value === undefined) delete process.env[name]; else process.env[name] = value; }
  }
}));
