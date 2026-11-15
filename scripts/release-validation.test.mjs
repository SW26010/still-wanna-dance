import test from 'node:test';
import assert from 'node:assert/strict';
import { checkValidationReuse } from './release-validation.mjs';

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
