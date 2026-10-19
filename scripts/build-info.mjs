// Shared linker identity for development, formal and corresponding-source builds.
export function identityFlags({ kind = 'development', version = 'dev', builtAtUtc = new Date().toISOString(), revision = '' } = {}) {
  if (!['development', 'formal', 'rebuild'].includes(kind) || !/^[a-zA-Z0-9][a-zA-Z0-9._-]*$/.test(version) || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z$/.test(builtAtUtc)) throw Error('Invalid executable build identity');
  if (kind === 'development' && /^v?\d+\.\d+\.\d+(?:[-+].*)?$/.test(version)) throw Error('Versioned releases require the formal release pipeline');
  if (kind !== 'development' && !/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version)) throw Error('Formal/rebuilt identity requires a version tag');
  if ((kind === 'rebuild' && !/^[a-f0-9]{40}$/.test(revision)) || (kind !== 'rebuild' && revision !== '')) throw Error('Only source rebuilds supply the recorded source revision');
  return Object.entries({ Kind: kind, Version: version, BuiltAtUTC: builtAtUtc, SourceRevision: revision }).filter(([, value]) => value).map(([key, value]) => `-X still-wanna-dance/internal/buildinfo.${key}=${value}`).join(' ');
}

export function checkBuildMetadata(metadata, expected) {
  for (const key of ['kind', 'version', 'revision', 'dirty', 'target', 'toolchain']) {
    if (metadata[key] !== expected[key]) throw Error(`Executable build identity mismatch: ${key}`);
  }
  if (!/^\d{4}-\d\d-\d\dT.*Z$/.test(metadata.builtAtUtc) || !Number.isFinite(Date.parse(metadata.builtAtUtc))) throw Error('Missing executable build time');
  return metadata;
}
