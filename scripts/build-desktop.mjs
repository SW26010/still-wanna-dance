import path from 'node:path';
import { desktopEntry, desktopBuildFlags, goEnv, run } from './release-source.mjs';

const [output = 'bin/SWD.exe', version = 'dev'] = process.argv.slice(2);
run('go', ['build', ...desktopBuildFlags({ version }), '-buildvcs=true', '-o', path.resolve(output), desktopEntry], process.cwd(), goEnv());
