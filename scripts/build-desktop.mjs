import path from 'node:path';
import { desktopEntry, desktopBuildFlags, goEnv } from './release-source.mjs';
import { runGo } from './go-patches.mjs';

const [output = 'bin/SWD.exe', version = 'dev'] = process.argv.slice(2);
runGo(['build', ...desktopBuildFlags({ version }), '-buildvcs=true', '-o', path.resolve(output), desktopEntry], process.cwd(), goEnv());
