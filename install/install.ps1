# Jevlin installer for Windows PowerShell.
#
#   irm https://jevlin.ai/install.ps1 | iex
#
# Downloads the latest jevlin release for this machine from the project's
# GitHub releases, verifies it against that release's checksums.txt, puts the
# binary at $env:JEVLIN_HOME\bin\jevlin.exe (default ~\.jevlin\bin\jevlin.exe),
# and runs `jevlin setup` when a terminal is attached. Nothing else is written.
#
#   $env:JEVLIN_VERSION = "0.3.1"   install exactly that release
#   $env:JEVLIN_HOME = "C:\path"    the installation directory
#   $env:JEVLIN_NO_SETUP = "1"      install the binary and stop
$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo = "jevlinai/jevlin-go"
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  "AMD64" { "amd64" }
  "ARM64" { "arm64" }
  default { throw "jevlin install: unsupported architecture $($env:PROCESSOR_ARCHITECTURE)" }
}

# The latest release is the tag github.com/<repo>/releases/latest redirects to,
# the source install.sh reads too: no API, so no rate limit. The redirect is
# read, not followed. Invoke-WebRequest -MaximumRedirection 0 cannot read it in
# both editions: Windows PowerShell 5.1 returns the 302, but PowerShell 7 throws
# on it, and -ErrorAction does not stop the throw. An HttpWebRequest told not to
# follow redirects returns the 302 as its response on .NET Framework and .NET
# alike; install_test.ps1 runs it in both editions against a loopback 302.
$latestUrl = "https://github.com/$repo/releases/latest"
$version = $env:JEVLIN_VERSION
if (-not $version) {
  $request = [Net.WebRequest]::Create($latestUrl)
  $request.AllowAutoRedirect = $false
  $request.UserAgent = "jevlin-install"
  try {
    $response = $request.GetResponse()
  } catch {
    $cause = $_.Exception
    while ($cause.InnerException) { $cause = $cause.InnerException }
    throw "jevlin install: could not find the latest release at ${latestUrl}: $($cause.Message)"
  }
  try {
    $status = [int]$response.StatusCode
    $location = $response.GetResponseHeader("Location")
  } finally {
    $response.Close()
  }
  if (-not $location) { throw "jevlin install: could not find the latest release: $latestUrl answered $status, not a redirect to a tag" }
  $version = ($location -split "/tag/v")[-1]
}
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw "jevlin install: not a release version: $version" }

# What `jevlin version` printed. It throws with the reason when the binary does
# not start or exits non-zero.
function Get-JevlinVersion([string]$path) {
  $out = & $path version
  if ($LASTEXITCODE -ne 0) { throw "version exited $LASTEXITCODE" }
  "$out"
}

$home_ = if ($env:JEVLIN_HOME) { $env:JEVLIN_HOME } else { Join-Path $HOME ".jevlin" }
$bindir = Join-Path $home_ "bin"
$name = "jevlin_${version}_windows_${arch}.zip"
$base = "https://github.com/$repo/releases/download/v$version"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("jevlin-install-" + [Guid]::NewGuid().ToString("n"))
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
  Write-Host "jevlin $version for windows/$arch"
  Invoke-WebRequest -Uri "$base/$name" -OutFile (Join-Path $tmp $name) -UseBasicParsing
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp "checksums.txt") -UseBasicParsing

  Invoke-WebRequest -Uri "$base/checksums.txt.sig" -OutFile (Join-Path $tmp "checksums.txt.sig") -UseBasicParsing

  # The release signature: checksums.txt.sig is one base64 line, an Ed25519
  # signature by the project's release key over the domain string and
  # checksums.txt's exact bytes, the rule `jevlin upgrade` and the npm
  # installer apply. It is a second layer on top of the sha256 check below,
  # which is mandatory and is what the manual install relies on alone. .NET has
  # no Ed25519, so node checks it when present: a signature that fails with a
  # working node stops the install, and a machine without node continues on the
  # checksum.
  if ($env:JEVLIN_SKIP_SIGNATURE) {
    Write-Host "warning: release signature not checked (JEVLIN_SKIP_SIGNATURE); relying on the checksum alone"
  } else {
    $releaseKeys = @("NQZgr4RzDpDvJwHmnnQhsssZ0G4TDSFuotlWmT05fmc=")
    $domain = [Text.Encoding]::UTF8.GetBytes("jevlin release checksums.txt v1`n")
    $sums = [IO.File]::ReadAllBytes((Join-Path $tmp "checksums.txt"))
    [IO.File]::WriteAllBytes((Join-Path $tmp "signed.msg"), $domain + $sums)
    $sigText = (Get-Content (Join-Path $tmp "checksums.txt.sig") -Raw).Trim()
    $sig = [Convert]::FromBase64String($sigText)
    if ($sig.Length -ne 64) { throw "jevlin install: checksums.txt.sig is not one Ed25519 signature" }
    [IO.File]::WriteAllBytes((Join-Path $tmp "sig.bin"), $sig)
    $node = Get-Command node -ErrorAction SilentlyContinue
    $verified = $false
    $worked = $false
    if ($node) {
      # The check is written to a file rather than passed with -e: Windows
      # PowerShell 5.1 drops the double quotes inside an argument it hands a
      # native program, so node received require(crypto) and failed to parse.
      # Exit 0 is a good signature, 1 a bad one, 2 a node that could not check
      # (one without Ed25519, say), which counts as no verifier.
      $script = 'try{const c=require("crypto"),fs=require("fs");const [key,msg,sig]=process.argv.slice(2);const spki=Buffer.concat([Buffer.from("302a300506032b6570032100","hex"),Buffer.from(key,"base64")]);process.exit(c.verify(null,fs.readFileSync(msg),{key:spki,format:"der",type:"spki"},fs.readFileSync(sig))?0:1)}catch(e){process.exit(2)}'
      $verifyJs = Join-Path $tmp "verify.js"
      [IO.File]::WriteAllText($verifyJs, $script, [Text.Encoding]::ASCII)
      foreach ($k in $releaseKeys) {
        & $node.Source $verifyJs $k (Join-Path $tmp "signed.msg") (Join-Path $tmp "sig.bin")
        if ($LASTEXITCODE -eq 0) { $verified = $true; $worked = $true; break }
        if ($LASTEXITCODE -eq 1) { $worked = $true }
      }
    }
    if ($verified) {
      Write-Host "release signature verified (node)"
    } elseif ($worked) {
      throw "jevlin install: checksums.txt.sig does not verify against the project's release key; nothing was installed"
    } else {
      Write-Host "release signature not checked (no verifier on this machine: node); relying on the checksum alone, as the manual install does"
    }
  }

  $line = Get-Content (Join-Path $tmp "checksums.txt") | Where-Object { $_ -match "  $([regex]::Escape($name))$" }
  if (-not $line) { throw "jevlin install: checksums.txt has no entry for $name" }
  $expected = ($line -split " ")[0].ToLower()
  $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $name)).Hash.ToLower()
  if ($actual -ne $expected) { throw "jevlin install: checksum mismatch for ${name}: the download is not the release's; nothing was installed" }

  Expand-Archive -LiteralPath (Join-Path $tmp $name) -DestinationPath $tmp -Force
  New-Item -ItemType Directory -Path $bindir -Force | Out-Null
  $exe = Join-Path $bindir "jevlin.exe"
  $hadOne = Test-Path -LiteralPath $exe
  # This run's names beside jevlin.exe are its own, so a displaced copy an
  # earlier run could not remove, because the jevlin it held was still running,
  # never stands in the way.
  $run = [Guid]::NewGuid().ToString("n").Substring(0, 12)
  $staged = Join-Path $bindir "jevlin.new-$run.exe"
  $displaced = Join-Path $bindir "jevlin.exe.displaced-$run"

  # The new binary runs before the installed one is touched, beside it under
  # this run's name: not in the temporary directory, which a policy can forbid
  # running programs from where the bin directory is allowed.
  Move-Item -LiteralPath (Join-Path $tmp "jevlin.exe") -Destination $staged
  try {
    $null = Get-JevlinVersion $staged
  } catch {
    Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
    $kept = if ($hadOne) { "$exe was left as it was" } else { "nothing was installed" }
    throw "jevlin install: the downloaded jevlin.exe does not run here ($($_.Exception.Message)); $kept"
  }

  # A running jevlin.exe cannot be overwritten or deleted, but it can be
  # renamed. So the installed one is moved aside, the new one takes its place,
  # and the old one is kept until the new one has run from there, to be put
  # back if it does not.
  if ($hadOne) {
    try {
      Move-Item -LiteralPath $exe -Destination $displaced
    } catch {
      Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
      throw "jevlin install: could not move the installed $exe aside ($($_.Exception.Message)); it was left as it was"
    }
  }
  $failed = $null
  try {
    Move-Item -LiteralPath $staged -Destination $exe
    $reported = Get-JevlinVersion $exe
  } catch {
    $failed = $_.Exception.Message
  }
  if ($failed) {
    Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
    try {
      if (Test-Path -LiteralPath $exe) { Remove-Item -LiteralPath $exe -Force }
      if ($hadOne) { Move-Item -LiteralPath $displaced -Destination $exe }
    } catch {
      if ($hadOne) {
        throw "jevlin install: the new jevlin.exe does not run from $exe ($failed), and putting the previous one back failed ($($_.Exception.Message)): the previous jevlin.exe is at $displaced"
      }
      throw "jevlin install: the new jevlin.exe does not run from $exe ($failed), and removing it failed ($($_.Exception.Message)); delete $exe"
    }
    $back = if ($hadOne) { "the previous jevlin.exe was put back" } else { "nothing was installed" }
    throw "jevlin install: the new jevlin.exe does not run from $exe ($failed); $back"
  }
  Write-Host "installed $exe ($reported)"
  if ($hadOne) {
    Remove-Item -LiteralPath $displaced -Force -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $displaced) {
      Write-Host "the previous jevlin.exe could not be removed, most likely because it is still running: delete $displaced once it exits"
    }
  }
} finally {
  Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

if ($env:JEVLIN_NO_SETUP) {
  Write-Host "next: $exe setup"
} elseif ([Environment]::UserInteractive -and -not [Console]::IsInputRedirected) {
  Write-Host ""
  & $exe setup
} else {
  Write-Host "no terminal attached; run: $exe setup"
}
