import test from 'node:test';
import assert from 'node:assert/strict';
import { identityFlags, checkBuildMetadata } from './build-info.mjs';

test('build arguments reject flag injection and development labels posing as releases', () => {
  assert.match(identityFlags({version:'dev-test'}), /Kind=development/);
  for (const version of ['v1.2.3', '1.2.3', 'dev -X injected=yes', 'dev\n-X injected=yes', '../dev']) assert.throws(() => identityFlags({version}));
  assert.throws(() => identityFlags({kind:'formal', version:'dev-test'}));
  assert.throws(() => identityFlags({kind:'formal', version:'v1.2.3', revision:'a'.repeat(40)}));
  assert.match(identityFlags({kind:'rebuild', version:'v1.2.3', revision:'a'.repeat(40)}), /SourceRevision=/);
  assert.throws(() => identityFlags({kind:'rebuild', version:'v1.2.3'}));
});

test('release diagnostics must agree with the independently verified binary/source identity', () => {
  const expected = {kind:'formal', version:'v1.2.3', revision:'a'.repeat(40), dirty:false, target:'windows/amd64', toolchain:'go1.25.0'};
  const metadata = {...expected, builtAtUtc:'2026-10-19T00:00:00Z'};
  assert.equal(checkBuildMetadata(metadata, expected), metadata);
  for (const patch of [{kind:'development'}, {dirty:true}, {revision:'b'.repeat(40)}, {version:'v1.2.4'}, {target:'linux/amd64'}, {toolchain:'unknown'}, {builtAtUtc:''}]) assert.throws(() => checkBuildMetadata({...metadata,...patch}, expected));
});
