#!/usr/bin/env node
// Launcher for heall. The program itself is a single native binary; this
// script fetches the one for this machine the first time it is needed,
// checks it against the published checksum, keeps it in the user's cache,
// and from then on just runs it.
//
//   HEALL_BASE_URL   download from here instead of the GitHub release
"use strict";

const { spawn, spawnSync } = require("node:child_process");
const crypto = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const { version } = require("../package.json");
const REPO = "TheCodeHeist-Coder/heall";

function fail(message) {
  console.error(`heall: ${message}`);
  process.exit(1);
}

function target() {
  const system = { linux: "linux", darwin: "darwin" }[process.platform];
  const cpu = { x64: "amd64", arm64: "arm64" }[process.arch];
  if (!system) fail(`${process.platform} is not supported; heall runs on Linux and macOS (on Windows, use WSL)`);
  if (!cpu) fail(`${process.arch} processors are not supported`);
  return `heall_${system}_${cpu}.tar.gz`;
}

async function download(url) {
  const res = await fetch(url, { redirect: "follow" });
  if (!res.ok) throw new Error(`${url}: HTTP ${res.status}`);
  return Buffer.from(await res.arrayBuffer());
}

async function install(binary) {
  const name = target();
  const base = process.env.HEALL_BASE_URL || `https://github.com/${REPO}/releases/download/v${version}`;
  console.error(`heall: first run, downloading v${version} for this machine`);
  let archive, checksums;
  try {
    [archive, checksums] = await Promise.all([download(`${base}/${name}`), download(`${base}/checksums.txt`)]);
  } catch (err) {
    fail(`could not download heall: ${err.message}`);
  }

  // Nothing is unpacked, let alone run, unless it matches the checksum.
  const line = checksums.toString().split("\n").find((l) => l.trim().endsWith(name));
  const want = line && line.trim().split(/\s+/)[0];
  const got = crypto.createHash("sha256").update(archive).digest("hex");
  if (!want) fail(`no checksum is published for ${name}`);
  if (want !== got) fail("the download does not match its checksum; not installing");

  const dir = path.dirname(binary);
  fs.mkdirSync(dir, { recursive: true });
  const tmp = fs.mkdtempSync(path.join(dir, "download-"));
  try {
    fs.writeFileSync(path.join(tmp, name), archive);
    const tar = spawnSync("tar", ["-xzf", name], { cwd: tmp, stdio: ["ignore", "ignore", "inherit"] });
    if (tar.status !== 0) fail("could not unpack the download (is tar installed?)");
    fs.chmodSync(path.join(tmp, "heall"), 0o755);
    // Renamed into place, so a second heall starting now never runs half a file.
    fs.renameSync(path.join(tmp, "heall"), binary);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

async function main() {
  const cache = process.env.XDG_CACHE_HOME || path.join(os.homedir(), ".cache");
  const binary = path.join(cache, "heall", "bin", `heall-${version}`);
  if (!fs.existsSync(binary)) await install(binary);

  const child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });
  // Ctrl-C reaches heall directly, since it shares the terminal. This script
  // must outlive it, so that heall can clean up its worktrees and containers.
  for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
    process.on(signal, () => {
      if (signal !== "SIGINT") child.kill(signal);
    });
  }
  child.on("error", (err) => fail(`could not start ${binary}: ${err.message}`));
  child.on("exit", (code, signal) => process.exit(signal ? 1 : code ?? 1));
}

main();
