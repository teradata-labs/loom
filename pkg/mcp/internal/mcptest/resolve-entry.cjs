// Copyright 2026 Teradata
// SPDX-License-Identifier: Apache-2.0

const fs = require('node:fs');
const path = require('node:path');

const packageName = process.argv[2];

// npm exec puts the selected package's node_modules/.bin on PATH. Read its
// manifest rather than executing the bin shim (a .cmd wrapper on Windows).
function resolveEntry() {
  for (const binDir of (process.env.PATH ?? '').split(path.delimiter)) {
    const modules = path.dirname(binDir);
    if (path.basename(binDir) !== '.bin' || path.basename(modules) !== 'node_modules') continue;

    const packageDir = path.join(modules, packageName);
    let manifest;
    try {
      manifest = JSON.parse(fs.readFileSync(path.join(packageDir, 'package.json'), 'utf8'));
    } catch (err) {
      if (err.code === 'ENOENT' || err.code === 'ENOTDIR') continue;
      throw err;
    }

    // The reference servers each expose a single JavaScript entry point.
    const bins = typeof manifest.bin === 'string' ? [manifest.bin] : Object.values(manifest.bin ?? {});
    if (manifest.name !== packageName || bins.length !== 1 || typeof bins[0] !== 'string' || !bins[0]) {
      throw new Error(`Expected one executable entry for ${packageName} in ${packageDir}`);
    }
    const entry = fs.realpathSync(path.resolve(packageDir, bins[0]));
    if (!fs.statSync(entry).isFile()) throw new Error(`Expected a file at ${entry}`);
    return entry;
  }
  throw new Error(`Could not find ${packageName} in npm's executable search path`);
}

process.stdout.write(JSON.stringify({ command: process.execPath, entry: resolveEntry() }));
