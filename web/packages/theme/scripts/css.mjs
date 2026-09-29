import { writeFile } from 'node:fs/promises';
import { cssVariables } from '../dist/tokens.js';

const block = (selector, mode) => `${selector} {\n  color-scheme: ${mode};\n${Object.entries(cssVariables(mode)).map(([key, value]) => `  ${key}: ${value};`).join('\n')}\n}\n`;
await writeFile(new URL('../dist/style.css', import.meta.url),
  block(':root, .dark, [data-theme="dark"]', 'dark') + block('.light, [data-theme="light"]', 'light'));
