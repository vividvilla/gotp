#!/usr/bin/env node

const { spawnSync } = require("node:child_process");

const packages = {
  "darwin-arm64": "@vividvilla/gotp-darwin-arm64",
  "darwin-x64": "@vividvilla/gotp-darwin-x64",
  "linux-arm64": "@vividvilla/gotp-linux-arm64",
  "linux-x64": "@vividvilla/gotp-linux-x64",
  "win32-x64": "@vividvilla/gotp-win32-x64",
};
const platform = `${process.platform}-${process.arch}`;
const packageName = packages[platform];

if (!packageName) {
  console.error(`gotp does not provide a binary for ${platform}`);
  process.exit(1);
}

let executable;
try {
  executable = require.resolve(
    `${packageName}/bin/gotp${process.platform === "win32" ? ".exe" : ""}`,
  );
} catch {
  console.error(`gotp's optional package for ${platform} was not installed`);
  process.exit(1);
}

const result = spawnSync(executable, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}
process.exit(result.status ?? 1);
