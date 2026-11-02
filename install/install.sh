#!/bin/sh
# Jevlin installer for macOS, Linux and WSL.
#
#   curl -fsSL https://jevlin.ai/install.sh | sh
#
# Downloads the latest jevlin release for this machine from the project's
# GitHub releases, verifies it against that release's checksums.txt, puts the
# binary at $JEVLIN_HOME/bin/jevlin (default ~/.jevlin/bin/jevlin), and runs
# `jevlin setup` when a terminal is attached. Nothing else is written.
#
#   JEVLIN_VERSION=0.3.1   install exactly that release instead of the latest
#   JEVLIN_HOME=/path      the installation directory (default ~/.jevlin)
#   JEVLIN_NO_SETUP=1      install the binary and stop
set -eu

REPO="jevlinai/jevlin-go"

say() { printf '%s\n' "$*" >&2; }
die() { say "jevlin install: $*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

need curl
need tar

os=$(uname -s)
case "$os" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported OS: $os (use the Windows installer, or the npm package)" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac

version="${JEVLIN_VERSION:-}"
if [ -z "$version" ]; then
  # The releases/latest page redirects to the latest tag; no API, no rate limit.
  latest=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") || die "could not find the latest release"
  version=${latest##*/tag/v}
  [ "$version" != "$latest" ] || die "could not read the latest version from $latest"
fi
case "$version" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) die "not a release version: $version" ;;
esac

home="${JEVLIN_HOME:-$HOME/.jevlin}"
bindir="$home/bin"
name="jevlin_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/v$version"

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t jevlin)
trap 'rm -rf "$tmp"' EXIT

say "jevlin $version for $os/$arch"
curl -fsSL -o "$tmp/$name" "$base/$name" || die "download failed: $base/$name"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "download failed: $base/checksums.txt"
curl -fsSL -o "$tmp/checksums.txt.sig" "$base/checksums.txt.sig" || die "download failed: $base/checksums.txt.sig (releases before 0.4.1 are unsigned; install 0.4.1 or newer)"

# The release signature: checksums.txt.sig is one base64 line, an Ed25519
# signature by the project's release key over the domain string and
# checksums.txt's exact bytes, the same rule `jevlin upgrade` and the npm
# installer apply. The key is the one those carry at the same release.
#
# It is a second layer on top of the sha256 check below, which is mandatory and
# is what the manual install (download the archive, compare its sha256 with
# checksums.txt) relies on alone. So the signature is checked with whatever can
# check it on this machine: a signature that fails with a tool that works stops
# the install, and a machine with no such tool continues on the checksum.
RELEASE_PUBLIC_KEYS="NQZgr4RzDpDvJwHmnnQhsssZ0G4TDSFuotlWmT05fmc="
SIGNATURE_DOMAIN="jevlin release checksums.txt v1"

# Each verifier returns 0 when the signature verifies, 1 when it does not, and
# 2 when it could not run at all (no such tool, or a tool without Ed25519), so
# a missing tool is never mistaken for a bad signature.
verify_with_openssl() {
  command -v openssl >/dev/null 2>&1 || return 2
  # LibreSSL (macOS) and OpenSSL before 1.1.1 have no raw Ed25519 verify.
  openssl pkeyutl -help 2>&1 | grep -q rawin || return 2
  # An Ed25519 SubjectPublicKeyInfo is a fixed DER prefix and the 32 key bytes.
  { printf '\060\052\060\005\006\003\053\145\160\003\041\000'; printf '%s' "$1" | base64 -d; } > "$tmp/pub.der" 2>/dev/null || return 2
  openssl pkey -pubin -inform DER -in "$tmp/pub.der" -out "$tmp/pub.pem" >/dev/null 2>&1 || return 2
  openssl pkeyutl -verify -pubin -inkey "$tmp/pub.pem" -rawin -in "$tmp/signed.msg" -sigfile "$tmp/sig.bin" >/dev/null 2>&1
}
verify_with_node() {
  command -v node >/dev/null 2>&1 || return 2
  node -e '
    try {
      const c = require("crypto"), fs = require("fs");
      const [key, msg, sig] = process.argv.slice(1);
      const raw = Buffer.from(key, "base64");
      const spki = Buffer.concat([Buffer.from("302a300506032b6570032100", "hex"), raw]);
      const ok = c.verify(null, fs.readFileSync(msg), { key: spki, format: "der", type: "spki" }, fs.readFileSync(sig));
      process.exit(ok ? 0 : 1);
    } catch (e) { process.exit(2); }
  ' "$1" "$tmp/signed.msg" "$tmp/sig.bin" >/dev/null 2>&1
}
# python3 is only run when it is a real interpreter: on a Mac without the
# command line tools /usr/bin/python3 is a stub that opens an install dialog.
python_usable() {
  command -v python3 >/dev/null 2>&1 || return 1
  if [ "$(uname -s)" = Darwin ] && [ "$(command -v python3)" = /usr/bin/python3 ]; then
    xcode-select -p >/dev/null 2>&1 || return 1
  fi
}
verify_with_python_cryptography() {
  python_usable || return 2
  python3 - "$1" "$tmp/signed.msg" "$tmp/sig.bin" <<'PY' >/dev/null 2>&1
import base64, sys
try:
    from cryptography.exceptions import InvalidSignature
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
except Exception:
    sys.exit(2)
try:
    key = Ed25519PublicKey.from_public_bytes(base64.b64decode(sys.argv[1]))
    key.verify(open(sys.argv[3], "rb").read(), open(sys.argv[2], "rb").read())
except InvalidSignature:
    sys.exit(1)
except Exception:
    sys.exit(2)
PY
}
# Ed25519 verification from RFC 8032 section 6 (the public-domain reference
# implementation, verify only), on hashlib alone, for a python3 that has no
# cryptography package.
verify_with_python_builtin() {
  python_usable || return 2
  python3 - "$1" "$tmp/signed.msg" "$tmp/sig.bin" <<'PY' >/dev/null 2>&1
import base64, hashlib, sys

p = 2**255 - 19
d = -121665 * pow(121666, p - 2, p) % p
q = 2**252 + 27742317777372353535851937790883648493
sqrt_m1 = pow(2, (p - 1) // 4, p)

def inv(x):
    return pow(x, p - 2, p)

def recover_x(y, sign):
    if y >= p:
        return None
    x2 = (y * y - 1) * inv(d * y * y + 1)
    if x2 == 0:
        return None if sign else 0
    x = pow(x2, (p + 3) // 8, p)
    if (x * x - x2) % p != 0:
        x = x * sqrt_m1 % p
    if (x * x - x2) % p != 0:
        return None
    if (x & 1) != sign:
        x = p - x
    return x

def add(P, Q):
    A, B = (P[1] - P[0]) * (Q[1] - Q[0]) % p, (P[1] + P[0]) * (Q[1] + Q[0]) % p
    C, D = 2 * P[3] * Q[3] * d % p, 2 * P[2] * Q[2] % p
    E, F, G, H = B - A, D - C, D + C, B + A
    return (E * F % p, G * H % p, F * G % p, E * H % p)

def mul(s, P):
    Q = (0, 1, 1, 0)
    while s > 0:
        if s & 1:
            Q = add(Q, P)
        P = add(P, P)
        s >>= 1
    return Q

def equal(P, Q):
    return (P[0] * Q[2] - Q[0] * P[2]) % p == 0 and (P[1] * Q[2] - Q[1] * P[2]) % p == 0

def decompress(s):
    if len(s) != 32:
        return None
    y = int.from_bytes(s, "little")
    sign = y >> 255
    y &= (1 << 255) - 1
    x = recover_x(y, sign)
    return None if x is None else (x, y, 1, x * y % p)

gy = 4 * inv(5) % p
gx = recover_x(gy, 0)
G = (gx, gy, 1, gx * gy % p)

def verify(public, msg, sig):
    if len(public) != 32 or len(sig) != 64:
        return False
    A = decompress(public)
    Rs = sig[:32]
    R = decompress(Rs)
    if A is None or R is None:
        return False
    s = int.from_bytes(sig[32:], "little")
    if s >= q:
        return False
    h = int.from_bytes(hashlib.sha512(Rs + public + msg).digest(), "little") % q
    return equal(mul(s, G), add(R, mul(h, A)))

try:
    key = base64.b64decode(sys.argv[1])
    msg = open(sys.argv[2], "rb").read()
    sig = open(sys.argv[3], "rb").read()
    ok = verify(key, msg, sig)
except Exception:
    sys.exit(2)
sys.exit(0 if ok else 1)
PY
}

if [ -n "${JEVLIN_SKIP_SIGNATURE:-}" ]; then
  say "warning: release signature not checked (JEVLIN_SKIP_SIGNATURE); relying on the checksum alone"
else
  { printf '%s\n' "$SIGNATURE_DOMAIN"; cat "$tmp/checksums.txt"; } > "$tmp/signed.msg"
  tr -d '\r\n' < "$tmp/checksums.txt.sig" | base64 -d > "$tmp/sig.bin" 2>/dev/null || die "checksums.txt.sig is not base64"
  [ "$(wc -c < "$tmp/sig.bin" | tr -d ' ')" = "64" ] || die "checksums.txt.sig is not one Ed25519 signature"

  verified=
  worked=
  for tool in openssl node python3-cryptography python3-builtin; do
    for key in $RELEASE_PUBLIC_KEYS; do
      rc=0
      case "$tool" in
        openssl) verify_with_openssl "$key" || rc=$? ;;
        node) verify_with_node "$key" || rc=$? ;;
        python3-cryptography) verify_with_python_cryptography "$key" || rc=$? ;;
        python3-builtin) verify_with_python_builtin "$key" || rc=$? ;;
      esac
      [ "$rc" = 2 ] && break
      worked=1
      if [ "$rc" = 0 ]; then verified=$tool; break 2; fi
    done
  done
  if [ -n "$verified" ]; then
    say "release signature verified ($verified)"
  elif [ -n "$worked" ]; then
    die "checksums.txt.sig does not verify against the project's release key; nothing was installed"
  else
    say "release signature not checked (no verifier on this machine: node, openssl 3 or python3); relying on the checksum alone, as the manual install does"
  fi
fi

expected=$(grep "  $name\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$expected" ] || die "checksums.txt has no entry for $name"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$name" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$name" | cut -d' ' -f1)
else
  die "sha256sum or shasum is required to verify the download"
fi
[ "$actual" = "$expected" ] || die "checksum mismatch for $name: the download is not the release's; nothing was installed"

tar -xzf "$tmp/$name" -C "$tmp" jevlin
mkdir -p "$bindir"
chmod 700 "$home" "$bindir" 2>/dev/null || true
# A fresh name beside the installed binary, run there before the installed one
# is touched, then one rename: a jevlin that does not run here never replaces
# one that does, and a jevlin that is running keeps its old file. It runs beside
# the installed one, not in $tmp, because a temporary directory mounted noexec
# would refuse a binary that runs from the bin directory.
mv -f "$tmp/jevlin" "$bindir/jevlin.new"
chmod 755 "$bindir/jevlin.new"
if ! reported=$("$bindir/jevlin.new" version); then
  rm -f "$bindir/jevlin.new"
  if [ -e "$bindir/jevlin" ]; then
    die "the downloaded jevlin does not run here ($bindir/jevlin.new version failed); $bindir/jevlin was left as it was"
  fi
  die "the downloaded jevlin does not run here ($bindir/jevlin.new version failed); nothing was installed"
fi
mv -f "$bindir/jevlin.new" "$bindir/jevlin"
say "installed $bindir/jevlin ($reported)"

if [ -n "${JEVLIN_NO_SETUP:-}" ]; then
  say "next: $bindir/jevlin setup"
  exit 0
fi
# Open the terminal rather than test its mode: /dev/tty reads as readable and
# writable even in a process with no controlling terminal, such as an agent's
# shell, where opening it fails.
if (exec </dev/tty) 2>/dev/null; then
  say ""
  exec "$bindir/jevlin" setup </dev/tty
fi
say "no terminal attached; run: $bindir/jevlin setup"
