// SPDX-License-Identifier: GPL-3.0-only
// The workflow supplies authenticated API responses and a complete Git diff.
const deliveryFiles = new Set([
  'README.md', 'LICENSE', 'LICENSE-MIT', 'LICENSING.md', 'THIRD-PARTY-NOTICES.txt',
  ...['release-notices.mjs', 'release-notices.test.mjs', 'webui-source.mjs',
    'webui-source.test.mjs', 'dashboard-source.mjs', 'archive-source.ps1',
    'archive-source.test.mjs', 'archive-portable.ps1', 'package-portable.ps1',
    'package-notices.ps1', 'portable-files.json', 'portable-layout.test.mjs',
    'test-portable.ps1', 'test-portable-upgrade.ps1'].map(name => `scripts/${name}`),
]);

export function checkValidationReuse({ run, jobs, files, repository, workflowID, currentRunID }) {
  if (run.id === currentRunID || run.repository?.full_name !== repository ||
      run.head_repository?.full_name !== repository || run.workflow_id !== workflowID ||
      run.status !== 'completed' || run.conclusion !== 'success' ||
      !Number.isSafeInteger(run.run_attempt) || run.run_attempt < 1 ||
      !/^[a-f0-9]{40}$/.test(run.head_sha) ||
      !(run.event === 'workflow_dispatch' || (run.event === 'push' && /^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(run.head_branch)))) {
    throw Error('Validation must be a completed successful full check run in this repository');
  }
  // A reused run has skipped jobs and cannot become another reuse baseline.
  const required = [['test', 'Full product checks'], ...Array.from({ length: 6 }, (_, n) => [`race (${n})`, 'Full Windows race shard'])];
  for (const [name, step] of required) {
    const matches = jobs.filter(job => job.name === name);
    const attempt = Math.max(...matches.map(job => job.run_attempt));
    const latest = matches.filter(job => job.run_attempt === attempt);
    if (matches.some(job => !Number.isSafeInteger(job.run_attempt) || job.run_attempt < 1 || job.run_attempt > run.run_attempt) ||
        latest.length !== 1 || latest[0].status !== 'completed' || latest[0].conclusion !== 'success' ||
        !latest[0].steps?.some(item => item.name === step && item.status === 'completed' && item.conclusion === 'success')) {
      throw Error(`Validation did not complete ${name} / ${step}`);
    }
  }
  const changed = files.filter(file => !deliveryFiles.has(file) && !/^docs\//.test(file));
  if (changed.length) throw Error(`Product validation is stale; changed inputs: ${changed.join(', ')}`);
  return { runID: run.id, revision: run.head_sha, changedFiles: files };
}
