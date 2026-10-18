#!/usr/bin/env node
// postinstall: fetch the jevlin release binary for this platform.
//
// The npm package carries no binary of its own. Its version is the release
// tag; this script downloads exactly that tag's archive from GitHub
// Releases, checks that checksums.txt is signed by a release key this
// package carries and that the archive matches it, and unpacks the binary
// beside this file. Nothing is executed from the download, and no
// credential is involved: the release assets are public.
//
// Set JEVLIN_SKIP_DOWNLOAD=1 to install the wrapper without fetching
// (CI images that mount their own binary), or JEVLIN_BINARY=/path to
// point the wrapper at one already on the machine.
"use strict"
const fs = require("node:fs")
const os = require("node:os")
const path = require("node:path")
const https = require("node:https")
const crypto = require("node:crypto")
const { execFileSync } = require("node:child_process")

const pkg = require("./package.json")
const REPO = "jevlinai/jevlin-go"

// RELEASE_PUBLIC_KEYS are the Ed25519 public keys, standard base64, whose
// signature over checksums.txt makes a release authentic: exactly the list
// internal/selfupdate/signature.go compiles into the binary at the same tag,
// which tools/releasecheck's preflight holds this one to. npm's provenance
// covers this script, not the archive it downloads; the signature is what
// ties the archive to the project's key. A package installs only its own
// release, so it needs only the keys that release was signed under.
const RELEASE_PUBLIC_KEYS = [
  "NQZgr4RzDpDvJwHmnnQhsssZ0G4TDSFuotlWmT05fmc=", // provisioned 2026-10-18
]

// SIGNATURE_DOMAIN prefixes the signed message, as selfupdate's
// signatureDomain does, so the signature is valid for nothing else.
const SIGNATURE_DOMAIN = "jevlin release checksums.txt v1\n"

// An Ed25519 SubjectPublicKeyInfo is this DER prefix and the key's 32 bytes.
const ED25519_SPKI_PREFIX = Buffer.from("302a300506032b6570032100", "hex")

// strictBase64 decodes text only when it is canonical, padded standard
// base64 of exactly size bytes: Buffer.from skips what it cannot read, so the
// decoding must encode back to the same text.
function strictBase64(text, size) {
  const raw = Buffer.from(text, "base64")
  return raw.length === size && raw.toString("base64") === text ? raw : null
}

// verifyChecksumsSignature is selfupdate.VerifyChecksumsSignature's rule:
// sig is one line of standard base64, an Ed25519 signature by one of keys
// over SIGNATURE_DOMAIN and checksums.txt's exact bytes.
function verifyChecksumsSignature(sums, sig, keys = RELEASE_PUBLIC_KEYS) {
  if (keys.length === 0) throw new Error("this package carries no release signing key, so it cannot verify any release")
  let text = sig.toString("latin1")
  if (text.endsWith("\n")) text = text.slice(0, -1)
  const raw = strictBase64(text, 64)
  if (!raw) throw new Error("checksums.txt.sig is not one base64 Ed25519 signature — refusing to unpack")
  const message = Buffer.concat([Buffer.from(SIGNATURE_DOMAIN, "utf8"), sums])
  for (const [i, encoded] of keys.entries()) {
    const key = strictBase64(encoded, 32)
    if (!key) throw new Error(`release key ${i} is not a base64 Ed25519 public key`)
    const publicKey = crypto.createPublicKey({ key: Buffer.concat([ED25519_SPKI_PREFIX, key]), format: "der", type: "spki" })
    if (crypto.verify(null, message, publicKey, raw)) return
  }
  throw new Error("checksums.txt is not signed by a release key this package trusts — refusing to unpack")
}

function platform() {
  const osName = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform]
  const arch = { x64: "amd64", arm64: "arm64" }[process.arch]
  if (!osName || !arch) throw new Error(`unsupported platform ${process.platform}/${process.arch}`)
  return { osName, arch }
}

function get(url, redirects = 5) {
  return new Promise((resolve, reject) => {
    https
      .get(url, { headers: { "user-agent": "jevlin-npm" } }, (res) => {
        if ([301, 302, 307, 308].includes(res.statusCode) && res.headers.location && redirects > 0) {
          res.resume()
          return resolve(get(res.headers.location, redirects - 1))
        }
        if (res.statusCode !== 200) {
          res.resume()
          return reject(new Error(`${url}: HTTP ${res.statusCode}`))
        }
        const chunks = []
        res.on("data", (c) => chunks.push(c))
        res.on("end", () => resolve(Buffer.concat(chunks)))
        res.on("error", reject)
      })
      .on("error", reject)
  })
}

async function main() {
  if (process.env.JEVLIN_SKIP_DOWNLOAD === "1" || process.env.JEVLIN_BINARY) return
  if (pkg.version === "0.0.0") {
    console.log("jevlin: development package, no release to download; set JEVLIN_BINARY")
    return
  }
  const { osName, arch } = platform()
  const ext = osName === "windows" ? "zip" : "tar.gz"
  const name = `jevlin_${pkg.version}_${osName}_${arch}.${ext}`
  const base = `https://github.com/${REPO}/releases/download/v${pkg.version}`

  const [archive, sums, sig] = await Promise.all([
    get(`${base}/${name}`),
    get(`${base}/checksums.txt`),
    get(`${base}/checksums.txt.sig`),
  ])
  verifyChecksumsSignature(sums, sig)
  const line = sums.toString("utf8").split("\n").find((l) => l.trim().endsWith(name))
  if (!line) throw new Error(`checksums.txt has no entry for ${name}`)
  const expected = line.trim().split(/\s+/)[0]
  const actual = crypto.createHash("sha256").update(archive).digest("hex")
  if (expected !== actual) throw new Error(`checksum FAILED for ${name} — refusing to unpack`)

  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "jevlin-"))
  const archivePath = path.join(tmp, name)
  fs.writeFileSync(archivePath, archive)
  if (ext === "zip") {
    execFileSync("powershell", ["-NoProfile", "-Command", `Expand-Archive -Path '${archivePath}' -DestinationPath '${tmp}' -Force`], { stdio: "inherit" })
  } else {
    execFileSync("tar", ["-xzf", archivePath, "-C", tmp], { stdio: "inherit" })
  }
  const bin = osName === "windows" ? "jevlin.exe" : "jevlin"
  const dest = path.join(__dirname, "bin", bin)
  fs.copyFileSync(path.join(tmp, bin), dest)
  if (osName !== "windows") fs.chmodSync(dest, 0o755)
  fs.rmSync(tmp, { recursive: true, force: true })
  console.log(`jevlin ${pkg.version} installed for ${osName}/${arch}`)
}

// npm runs this file as the postinstall; tools/releasecheck's tests require
// it to drive verifyChecksumsSignature, and nothing is downloaded then.
if (require.main === module) {
  main().catch((err) => {
    console.error(`jevlin: ${err.message}`)
    console.error("You can install the binary another way and set JEVLIN_BINARY to its path.")
    process.exit(1)
  })
}

module.exports = { verifyChecksumsSignature }
