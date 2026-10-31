#!/usr/bin/env node
// The npm entry point: hand every argument to the real binary and return
// its exit code. Nothing is interpreted here, so `npx jevlin search q`
// is exactly `jevlin search q`.
//
// Two things are added for the child: JEVLIN_LAUNCH=npm:<kind>, where
// kind is global, local or unknown, and, for a global install,
// JEVLIN_LAUNCH_BIN, the directory holding npm's `jevlin` command (the shim
// that runs this file), which is the directory a person's PATH needs. The
// binary this launcher starts lives inside the package, and that directory
// is never the one to put on PATH. `jevlin setup` writes this
// binary's path into every hook and skill, so it refuses to run from a copy
// npm or a project will discard — an exec cache or a project's own
// node_modules. Only this launcher knows which kind of install it is, and it
// works that out from paths alone; npm itself is never run to ask.
"use strict"
const path = require("node:path")
const { spawnSync } = require("node:child_process")

// npmGlobalRoots is where `npm root -g` would point, for the prefixes this
// process can know without running npm: one exported in the environment
// (npm_config_prefix), and the one implied by the node binary itself —
// <prefix>/bin/node on POSIX, <prefix>\node.exe on Windows. The global
// package directory is <prefix>/lib/node_modules on POSIX and
// <prefix>\node_modules on Windows.
function npmGlobalRoots(execPath, env, platform) {
  const p = platform === "win32" ? path.win32 : path.posix
  const prefixes = []
  if (env.npm_config_prefix) prefixes.push(env.npm_config_prefix)
  prefixes.push(platform === "win32" ? p.dirname(execPath) : p.dirname(p.dirname(execPath)))
  return prefixes.map((prefix) =>
    platform === "win32" ? p.join(prefix, "node_modules") : p.join(prefix, "lib", "node_modules"),
  )
}

// launchKind classifies the package directory (the directory holding this
// package's package.json) as a global install, a local one, or neither.
//
// The package is scoped, @jevlinsearch/jevlin, so npm puts it in its scope's
// directory: node_modules/@jevlinsearch/jevlin. The node_modules that says
// which install this is sits above the scope, not directly above the package.
//
// A prefix set in ~/.npmrc (prefix=~/.npm-global, the usual no-sudo setup)
// is invisible from here without running npm, so the global layout itself is
// the second witness: a global install puts its command shim at the prefix —
// <prefix>/bin/jevlin beside <prefix>/lib/node_modules on POSIX,
// <prefix>\jevlin.cmd beside <prefix>\node_modules on Windows — where a
// project install puts it under node_modules/.bin instead. The shim is named
// after the command package.json's bin declares, not after the package.
function launchKind(packageDir, execPath, env, platform, exists) {
  const p = platform === "win32" ? path.win32 : path.posix
  const norm = (s) => {
    const r = p.resolve(s)
    return platform === "win32" ? r.toLowerCase() : r
  }
  let modules = p.dirname(packageDir)
  if (p.basename(modules).startsWith("@")) modules = p.dirname(modules)
  for (const root of npmGlobalRoots(execPath, env, platform)) {
    if (norm(root) === norm(modules)) return "global"
  }
  if (p.basename(modules) !== "node_modules") return "unknown"
  const command = "jevlin"
  if (platform === "win32") {
    if (exists(p.join(p.dirname(modules), command + ".cmd"))) return "global"
  } else if (p.basename(p.dirname(modules)) === "lib") {
    if (exists(p.join(p.dirname(p.dirname(modules)), "bin", command))) return "global"
  }
  return "local"
}

// launchBinDir is the directory holding npm's `jevlin` command for a global
// install: <prefix>/bin on POSIX, where the package is at
// <prefix>/lib/node_modules/@jevlinsearch/jevlin, and <prefix> on Windows,
// where it is at <prefix>\node_modules\@jevlinsearch\jevlin. null when the
// layout is neither or the command is not there, so a guess is never passed
// on: setup then derives it from the path or says it cannot.
function launchBinDir(packageDir, platform, exists) {
  const p = platform === "win32" ? path.win32 : path.posix
  let modules = p.dirname(packageDir)
  if (p.basename(modules).startsWith("@")) modules = p.dirname(modules)
  const command = "jevlin"
  if (platform === "win32") {
    const dir = p.dirname(modules)
    return p.basename(modules) === "node_modules" && exists(p.join(dir, command + ".cmd")) ? dir : null
  }
  if (p.basename(modules) !== "node_modules" || p.basename(p.dirname(modules)) !== "lib") return null
  const dir = p.join(p.dirname(p.dirname(modules)), "bin")
  return exists(p.join(dir, command)) ? dir : null
}

module.exports = { launchKind, launchBinDir, npmGlobalRoots }

if (require.main === module) {
  const bin =
    process.env.JEVLIN_BINARY ||
    path.join(__dirname, process.platform === "win32" ? "jevlin.exe" : "jevlin")

  const exists = (f) => {
    try {
      require("node:fs").lstatSync(f)
      return true
    } catch {
      return false
    }
  }
  const kind = launchKind(path.dirname(__dirname), process.execPath, process.env, process.platform, exists)
  const env = Object.assign({}, process.env, { JEVLIN_LAUNCH: "npm:" + kind })
  // Only ever what this launcher computed: one inherited from outside is not
  // a statement about this install.
  delete env.JEVLIN_LAUNCH_BIN
  const binDir = kind === "global" ? launchBinDir(path.dirname(__dirname), process.platform, exists) : null
  if (binDir) env.JEVLIN_LAUNCH_BIN = binDir
  const result = spawnSync(bin, process.argv.slice(2), { stdio: "inherit", env })
  if (result.error) {
    console.error(`jevlin: cannot run ${bin}: ${result.error.message}`)
    console.error("Reinstall the package, or set JEVLIN_BINARY to a jevlin binary.")
    process.exit(1)
  }
  process.exit(result.status === null ? 1 : result.status)
}
