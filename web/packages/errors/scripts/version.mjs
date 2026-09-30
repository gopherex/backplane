// Writes src/version.ts from package.json: the SDK reports its version in every record.
import { readFileSync, writeFileSync } from 'node:fs';
const { version } = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'));
writeFileSync(new URL('../src/version.ts', import.meta.url), `// Generated from package.json by scripts/version.mjs.\nexport const SDK_VERSION = '${version}';\n`);
