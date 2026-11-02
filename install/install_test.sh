#!/bin/bash
# Offline test of install/install.sh against a fake release served from loopback.
#
#   install/install_test.sh
#
# Builds jevlin_<ver>_<os>_<arch>.tar.gz around a stub `jevlin`, a checksums.txt
# with the right sha256 and a checksums.txt.sig made with a throwaway Ed25519
# key (node signs it; the script copy under test trusts that key instead of the
# release key), serves them with python3's http.server, and runs the installer
# under restricted PATHs. Nothing contacts GitHub: the copy under test has its
# download base rewritten to the loopback server and is run with JEVLIN_VERSION
# set, so the latest-release lookup never runs.
set -u
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'kill $server 2>/dev/null; rm -rf "$work"' EXIT
ver=9.9.9
fail=0

case "$(uname -s)" in Darwin) os=darwin ;; Linux) os=linux ;; *) echo "unsupported OS"; exit 1 ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "unsupported arch"; exit 1 ;; esac

# Absolute tools the test itself uses; the installer under test gets only $bin.
node_bin=$(command -v node) || { echo "the test needs node to sign"; exit 1; }
py_bin=$(command -v python3) || { echo "the test needs python3 to serve"; exit 1; }

# A throwaway key, and its signature over the domain string and checksums.txt.
rel="$work/release/v$ver"; mkdir -p "$rel" "$work/stage"
# The stub records every call, so a case can tell whether the installer ran setup.
printf '#!/bin/sh\necho "$*" >> "%s"\necho "jevlin %s"\n' "$work/calls" "$ver" > "$work/stage/jevlin"; chmod 755 "$work/stage/jevlin"
name="jevlin_${ver}_${os}_${arch}.tar.gz"
tar -czf "$rel/$name" -C "$work/stage" jevlin
sum=$(shasum -a 256 "$rel/$name" 2>/dev/null || sha256sum "$rel/$name"); sum=${sum%% *}
printf '%s  %s\n' "$sum" "$name" > "$rel/checksums.txt"
"$node_bin" - "$rel/checksums.txt" "$work/pub.b64" "$work/good.sig" <<'JS'
const c = require("crypto"), fs = require("fs");
const [sums, pubOut, sigOut] = process.argv.slice(2);
const { publicKey, privateKey } = c.generateKeyPairSync("ed25519");
const raw = publicKey.export({ format: "der", type: "spki" }).subarray(-32);
fs.writeFileSync(pubOut, raw.toString("base64"));
const msg = Buffer.concat([Buffer.from("jevlin release checksums.txt v1\n"), fs.readFileSync(sums)]);
fs.writeFileSync(sigOut, c.sign(null, msg, privateKey).toString("base64") + "\n");
JS
pub=$(cat "$work/pub.b64")
head -c 64 /dev/urandom | base64 > "$work/bad.sig"

# The copy under test: the throwaway key instead of the release key, loopback
# instead of GitHub.
port=$((20000 + $$ % 20000))
sed -e "s|^RELEASE_PUBLIC_KEYS=.*|RELEASE_PUBLIC_KEYS=\"$pub\"|" \
    -e "s|^base=.*|base=\"http://127.0.0.1:$port/v\$version\"|" \
    "$root/install/install.sh" > "$work/install.sh"
cmp -s "$work/install.sh" "$root/install/install.sh" && { echo "FAIL: the copy was not changed"; exit 1; }
grep -q "127.0.0.1:$port" "$work/install.sh" || { echo "FAIL: base was not rewritten"; exit 1; }
grep -q "^RELEASE_PUBLIC_KEYS=\"$pub\"" "$work/install.sh" || { echo "FAIL: key was not rewritten"; exit 1; }

(cd "$work/release" && exec "$py_bin" -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1) &
server=$!
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
  curl -fs -o /dev/null "http://127.0.0.1:$port/v$ver/checksums.txt" && break; sleep 0.25
done

# A PATH of symlinks to the named tools only, so "no node" really is none.
mkpath() { # dir tool...
  local d=$1; shift; mkdir -p "$d"
  for t in "$@"; do ln -sf "$(command -v "$t")" "$d/$t"; done
}
base_tools="sh curl tar gzip uname mktemp rm mv mkdir chmod cat tr wc cut grep sed base64 printf dirname shasum sha256sum head ln"
mkpath "$work/p_node" $base_tools node
mkpath "$work/p_bare" $base_tools
# A stand-in openssl that is LibreSSL-like: no rawin.
mkdir -p "$work/p_libre"; cp -P "$work"/p_bare/* "$work/p_libre/" 2>/dev/null
printf '#!/bin/sh\necho "usage: pkeyutl [-help]" >&2\nexit 1\n' > "$work/p_libre/openssl"; chmod 755 "$work/p_libre/openssl"

run() { # name sigfile pathdir [env...]
  local label=$1 sig=$2 pdir=$3; shift 3
  cp "$sig" "$rel/checksums.txt.sig"
  rm -rf "$work/home"; mkdir "$work/home"
  env -i HOME="$work/home" JEVLIN_HOME="$work/home/j" JEVLIN_VERSION=$ver JEVLIN_NO_SETUP=1 PATH="$pdir" "$@" \
    /bin/sh "$work/install.sh" > "$work/out" 2>&1
  rc=$?
}
check() { # label want_rc want_text installed(yes|no)
  local label=$1 want=$2 text=$3 inst=$4 got=no
  [ -x "$work/home/j/bin/jevlin" ] && got=yes
  if [ "$rc" = "$want" ] && grep -qF -- "$text" "$work/out" && [ "$got" = "$inst" ]; then
    echo "ok   $label"
  else
    echo "FAIL $label (exit $rc, installed=$got, want exit $want, text '$text', installed=$inst)"; fail=1
  fi
  sed 's/^/       | /' "$work/out"
}

run a "$work/good.sig" "$work/p_node"
check "(a) node present: verified and installed" 0 "release signature verified (node)" yes

run b "$work/good.sig" "$work/p_bare"
check "(b) no node, no openssl 3, no python3: checksum alone, installed" 0 "release signature not checked (no verifier on this machine" yes

run b2 "$work/good.sig" "$work/p_libre"
check "(b2) LibreSSL-like openssl only: checksum alone, installed" 0 "release signature not checked (no verifier on this machine" yes

run c "$work/bad.sig" "$work/p_node"
check "(c) node present, bad signature: dies, nothing installed" 1 "does not verify against the project's release key; nothing was installed" no

run d "$work/bad.sig" "$work/p_bare"
check "(d) no verifier, garbage signature: the checksum decides, installed" 0 "relying on the checksum alone" yes

run e "$work/bad.sig" "$work/p_node" JEVLIN_SKIP_SIGNATURE=1
check "(e) JEVLIN_SKIP_SIGNATURE skips even with node: warning, installed" 0 "warning: release signature not checked (JEVLIN_SKIP_SIGNATURE)" yes

# The checksum stays mandatory: a wrong entry dies whatever the signature says.
cp "$rel/checksums.txt" "$work/checksums.good"
sed "s|^[0-9a-f]*  |$(printf '0%.0s' $(seq 64))  |" "$work/checksums.good" > "$rel/checksums.txt"
run f "$work/good.sig" "$work/p_bare"
check "(f) wrong checksum, no verifier: dies, nothing installed" 1 "checksum mismatch" no
: > "$rel/checksums.txt"
run g "$work/good.sig" "$work/p_bare"
check "(g) no checksum entry, no verifier: dies, nothing installed" 1 "checksums.txt has no entry" no
cp "$work/checksums.good" "$rel/checksums.txt"

# No JEVLIN_NO_SETUP and no controlling terminal, as in an agent's shell: /dev/tty
# still reads as readable and writable there, so the installer must open it to
# tell, print the command to run, exit 0, and never start setup. The run goes
# through python3's setsid in a forked child, which leaves it with no terminal on
# Linux and macOS alike.
: > "$work/calls"
rm -rf "$work/home"; mkdir "$work/home"
env -i HOME="$work/home" JEVLIN_HOME="$work/home/j" JEVLIN_VERSION=$ver PATH="$work/p_node" \
  "$py_bin" -c '
import os, sys
pid = os.fork()
if pid == 0:
    os.setsid()
    os.execv(sys.argv[1], sys.argv[1:])
_, status = os.waitpid(pid, 0)
sys.exit(os.waitstatus_to_exitcode(status))
' /bin/sh "$work/install.sh" < /dev/null > "$work/out" 2>&1
rc=$?
check "(j) no terminal, no JEVLIN_NO_SETUP: prints the setup command, exit 0" 0 \
  "no terminal attached; run: $work/home/j/bin/jevlin setup" yes
if grep -q '^setup' "$work/calls"; then
  echo "FAIL (j): the installer ran setup with no terminal"; fail=1
fi

# Each verifier on its own, both ways, so a verifier that always said yes would
# turn a refusal red. Under CI every case must run, and a missing prerequisite
# fails the step; a local run skips the case and says what it skipped.
missing() { # label what
  if [ -n "${CI:-}" ]; then
    echo "FAIL ($1): $2; CI must run this case"; fail=1
  else
    echo "skip ($1): $2"
  fi
}

# OpenSSL 1.1.1 or newer verifies raw Ed25519 (pkeyutl -rawin), and runs here as
# the only verifier: no node and no python3 on the PATH. Ubuntu's openssl has it;
# a Mac's may be Homebrew's, which is not on PATH.
ossl=
for cand in "$(command -v openssl 2>/dev/null)" /opt/homebrew/opt/openssl@3/bin/openssl /usr/local/opt/openssl@3/bin/openssl; do
  if [ -n "$cand" ] && [ -x "$cand" ] && "$cand" pkeyutl -help 2>&1 | grep -q rawin; then ossl=$cand; break; fi
done
if [ -n "$ossl" ]; then
  mkdir -p "$work/p_ossl"; cp -P "$work"/p_bare/* "$work/p_ossl/"; ln -sf "$ossl" "$work/p_ossl/openssl"
  run k "$work/good.sig" "$work/p_ossl"
  check "(k) OpenSSL as the only verifier: verified" 0 "release signature verified (openssl)" yes
  run k2 "$work/bad.sig" "$work/p_ossl"
  check "(k2) OpenSSL, bad signature: dies, nothing installed" 1 "does not verify against the project's release key" no
elif [ "$os" = linux ]; then
  missing "k, k2" "no OpenSSL 1.1.1 or newer here"
else
  echo "note (k, k2): no OpenSSL 1.1.1 or newer on this Mac; the Linux runner runs them"
fi

# macOS's own openssl is LibreSSL, which has no raw Ed25519 verify: it must be
# passed over for the next verifier, here node, rather than taken as one.
if [ "$os" = darwin ]; then
  if [ -x /usr/bin/openssl ] && ! /usr/bin/openssl pkeyutl -help 2>&1 | grep -q rawin; then
    mkdir -p "$work/p_libre_node"; cp -P "$work"/p_node/* "$work/p_libre_node/"; ln -sf /usr/bin/openssl "$work/p_libre_node/openssl"
    run m "$work/good.sig" "$work/p_libre_node"
    check "(m) LibreSSL is passed over for node: verified by node" 0 "release signature verified (node)" yes
  else
    missing m "no /usr/bin/openssl without raw Ed25519 verify"
  fi
fi

# python3 without the cryptography package: the built-in RFC 8032 check. A venv
# made without pip has no third-party packages; its bin directory goes first on
# the PATH, since a link to its python3 elsewhere would not find the venv.
if "$py_bin" -m venv --without-pip "$work/venv" >/dev/null 2>&1 && [ -x "$work/venv/bin/python3" ]; then
  if env -i HOME="$work/home" PATH="$work/venv/bin" "$work/venv/bin/python3" -c 'import cryptography' 2>/dev/null; then
    missing "h, i" "the venv can import cryptography"
  else
    run h "$work/good.sig" "$work/venv/bin:$work/p_bare"
    check "(h) python3 without cryptography: verified by the built-in check" 0 "release signature verified (python3-builtin)" yes
    run i "$work/bad.sig" "$work/venv/bin:$work/p_bare"
    check "(i) python3 without cryptography, bad signature: dies" 1 "does not verify against the project's release key" no
  fi
else
  missing "h, i" "python3 -m venv --without-pip failed"
fi

# python3 with the cryptography package, as the runners' python3 has it. A wrapper
# rather than a link runs it by its own path, so a venv or prefix it belongs to
# still applies; its user site, where pip --user puts packages, stays visible
# under the test's HOME.
userbase=$("$py_bin" -m site --user-base 2>/dev/null)
if "$py_bin" -c 'import cryptography' 2>/dev/null; then
  mkdir -p "$work/p_crypto"; cp -P "$work"/p_bare/* "$work/p_crypto/"
  printf '#!/bin/sh\nexec "%s" "$@"\n' "$py_bin" > "$work/p_crypto/python3"; chmod 755 "$work/p_crypto/python3"
  run l "$work/good.sig" "$work/p_crypto" PYTHONUSERBASE="$userbase"
  check "(l) python3 with cryptography: verified by cryptography" 0 "release signature verified (python3-cryptography)" yes
  run l2 "$work/bad.sig" "$work/p_crypto" PYTHONUSERBASE="$userbase"
  check "(l2) python3 with cryptography, bad signature: dies" 1 "does not verify against the project's release key" no
else
  missing "l, l2" "this python3 has no cryptography package"
fi

exit $fail
