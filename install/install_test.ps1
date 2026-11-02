# Offline test of install/install.ps1 against a fake release served from loopback.
#
#   powershell -NoProfile -ExecutionPolicy Bypass -File install\install_test.ps1
#   pwsh -NoProfile -File install\install_test.ps1
#
# The Windows counterpart of install_test.sh. It builds a stub jevlin.exe with Go
# (the stub logs its calls), zips it as jevlin_<ver>_windows_<arch>.zip, writes
# checksums.txt and a checksums.txt.sig made with a throwaway Ed25519 key (node
# signs it; the copy under test trusts that key instead of the release key), and
# serves them from a small Go server on 127.0.0.1, which also answers the
# latest-release URL with a 302 to the tag, as github.com does. The copy under
# test has its key, its latest-release URL and its download base rewritten to
# that server, and each rewrite is checked. Every case runs the copy the way the
# README says, `irm <url> | iex`, in a fresh process of the PowerShell running
# this file, so a run under powershell.exe tests Windows PowerShell 5.1 and one
# under pwsh.exe tests PowerShell 7. Nothing contacts GitHub or jevlin.ai.
$ErrorActionPreference = "Stop"
$fail = 0
$cases = 0
$ver = "9.9.9"

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
  Write-Host "install.ps1 is the Windows installer; this test runs on Windows"
  exit 1
}
$psExe = [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
# The installer picks its archive by its own process's architecture, and every
# case runs in a process of this same executable, so this one's is the one.
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  "AMD64" { "amd64" }
  "ARM64" { "arm64" }
  default { "" }
}
if (-not $arch) {
  Write-Host "unsupported architecture $($env:PROCESSOR_ARCHITECTURE)"
  exit 1
}
Write-Host ("PowerShell {0} ({1}), {2}, {3}" -f $PSVersionTable.PSVersion, $PSVersionTable.PSEdition, $env:PROCESSOR_ARCHITECTURE, $psExe)

# Absolute tools the test itself uses. Both are required, under CI and locally.
$goCmd = Get-Command go -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
$nodeCmd = Get-Command node -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $goCmd) { Write-Host "the test needs go to build the stub and the server"; exit 1 }
if (-not $nodeCmd) { Write-Host "the test needs node to sign"; exit 1 }
$goExe = $goCmd.Source
$nodeExe = $nodeCmd.Source

$tempRoot = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$work = Join-Path $tempRoot ("jevlin-install-test-" + [Guid]::NewGuid().ToString("n").Substring(0, 8))
New-Item -ItemType Directory -Path $work | Out-Null
$utf8 = New-Object Text.UTF8Encoding($false)

function Write-Text([string]$path, [string]$text) {
  [IO.File]::WriteAllText($path, $text, $utf8)
}

function Read-Text([string]$path) {
  if (Test-Path -LiteralPath $path) { return [IO.File]::ReadAllText($path) }
  return ""
}

# Sets the named environment variables ($null removes one) for the length of
# the script block, which is how a child process gets them.
function Use-Env([hashtable]$vars, [scriptblock]$body) {
  $saved = @{}
  foreach ($k in @($vars.Keys)) {
    $saved[$k] = [Environment]::GetEnvironmentVariable($k)
    [Environment]::SetEnvironmentVariable($k, $vars[$k])
  }
  try {
    & $body
  } finally {
    foreach ($k in @($saved.Keys)) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) }
  }
}

# The stub: `version` prints "jevlin <version>", every call is logged with the
# name it ran under, and its mode makes it fail. "fail" fails every call;
# "failinstalled" runs under any name but jevlin.exe, so it passes the check
# before the swap and fails the one after; "failheld" fails after starting a
# copy of itself from the same file, which then cannot be deleted. `hold` keeps
# it running, so the file it runs from cannot be deleted.
$goMod = @'
module jevlininstalltest

go 1.21
'@
$stubSource = @'
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	version = "0.0.0"
	mode    = "ok"
)

func main() {
	exe, _ := os.Executable()
	self := filepath.Base(exe)
	if p := os.Getenv("JEVLIN_TEST_STUB_LOG"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s %s %s\n", version, self, strings.Join(os.Args[1:], " "))
			f.Close()
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "hold" {
		time.Sleep(2 * time.Minute)
		return
	}
	if mode == "failheld" {
		// Detached, with no handle of this process's, so the installer waits
		// only for this one.
		holder := exec.Command(exe, "hold")
		holder.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008}
		_ = holder.Start()
	}
	if mode == "fail" || mode == "failheld" || (mode == "failinstalled" && strings.EqualFold(self, "jevlin.exe")) {
		fmt.Fprintln(os.Stderr, "stub: this jevlin does not run here")
		os.Exit(3)
	}
	fmt.Println("jevlin " + version)
}
'@
# The server: files from a directory, every request logged, and
# /jevlinai/jevlin-go/releases/latest answered as github.com answers it, with a
# 302 to the latest tag's page. It listens on a port the system picks and
# writes its origin to a file once it is listening.
$serverSource = @'
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
)

func main() {
	root, addrFile, logFile, latest := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	origin := "http://" + ln.Addr().String()
	files := http.FileServer(http.Dir(root))
	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "%s %s\n", r.Method, r.URL.Path)
			f.Close()
		}
		mu.Unlock()
		if r.URL.Path == "/jevlinai/jevlin-go/releases/latest" {
			http.Redirect(w, r, origin+"/jevlinai/jevlin-go/releases/tag/v"+latest, http.StatusFound)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".ps1") {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		files.ServeHTTP(w, r)
	})
	if err := os.WriteFile(addrFile+".tmp", []byte(origin), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Rename(addrFile+".tmp", addrFile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, http.Serve(ln, handler))
	os.Exit(1)
}
'@
# A throwaway key, its public half, and its signature over the domain string
# and each checksums.txt named.
$signSource = @'
const c = require("crypto"), fs = require("fs");
const [pubOut, ...pairs] = process.argv.slice(2);
const { publicKey, privateKey } = c.generateKeyPairSync("ed25519");
fs.writeFileSync(pubOut, publicKey.export({ format: "der", type: "spki" }).subarray(-32).toString("base64"));
for (let i = 0; i < pairs.length; i += 2) {
  const msg = Buffer.concat([Buffer.from("jevlin release checksums.txt v1\n"), fs.readFileSync(pairs[i])]);
  fs.writeFileSync(pairs[i + 1], c.sign(null, msg, privateKey).toString("base64") + "\n");
}
'@

$src = Join-Path $work "src"
New-Item -ItemType Directory -Path (Join-Path $src "stub"), (Join-Path $src "server") | Out-Null
Write-Text (Join-Path $src "go.mod") $goMod
Write-Text (Join-Path $src "stub\main.go") $stubSource
Write-Text (Join-Path $src "server\main.go") $serverSource
# No module is fetched and no toolchain downloaded: the sources import only the
# standard library.
$goEnv = @{ GOTOOLCHAIN = "local"; GOPROXY = "off"; GOFLAGS = $null; GOWORK = "off"; CGO_ENABLED = "0"; GOOS = $null; GOARCH = $null }

function Build-Stub([string]$out, [string]$stubVersion, [string]$stubMode) {
  $vars = $goEnv.Clone()
  $vars.GOOS = "windows"
  $vars.GOARCH = $arch
  Use-Env $vars {
    & $goExe -C $src build -trimpath -ldflags "-X main.version=$stubVersion -X main.mode=$stubMode" -o $out ./stub
    if ($LASTEXITCODE -ne 0) { throw "go build of the stub ($stubVersion, $stubMode) failed" }
  }
}

$srv = Join-Path $work "srv"
$downloads = Join-Path $srv "jevlinai\jevlin-go\releases\download"
# A release directory: the stub zipped under the installer's archive name and a
# checksums.txt with its sha256, in the format goreleaser writes.
function New-Release([string]$relVersion, [string]$stubMode) {
  $dir = Join-Path $downloads "v$relVersion"
  $stage = Join-Path $work "stage\$relVersion"
  New-Item -ItemType Directory -Path $dir, $stage -Force | Out-Null
  Build-Stub (Join-Path $stage "jevlin.exe") $relVersion $stubMode
  $zipName = "jevlin_${relVersion}_windows_${arch}.zip"
  Compress-Archive -LiteralPath (Join-Path $stage "jevlin.exe") -DestinationPath (Join-Path $dir $zipName)
  $sum = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $dir $zipName)).Hash.ToLower()
  Write-Text (Join-Path $dir "checksums.txt") "$sum  $zipName`n"
  return $dir
}

$server = $null
$holder = $null
try {
  $good = New-Release $ver "ok"
  # Pinned with JEVLIN_VERSION by the cases about the replace step.
  $failing = New-Release "9.9.10" "fail"
  $failingInstalled = New-Release "9.9.11" "failinstalled"
  $failingHeld = New-Release "9.9.12" "failheld"
  $tag = Join-Path $srv "jevlinai\jevlin-go\releases\tag"
  New-Item -ItemType Directory -Path $tag | Out-Null
  Write-Text (Join-Path $tag "v$ver") "<!doctype html><title>v$ver</title>`n"

  $signJs = Join-Path $work "sign.js"
  Write-Text $signJs $signSource
  $pubFile = Join-Path $work "pub.b64"
  $signArgs = @()
  foreach ($d in @($good, $failing, $failingInstalled, $failingHeld)) {
    $signArgs += (Join-Path $d "checksums.txt")
    $signArgs += (Join-Path $d "checksums.txt.sig")
  }
  & $nodeExe $signJs $pubFile @signArgs
  if ($LASTEXITCODE -ne 0) { throw "node could not make the throwaway key and signatures" }
  $pub = (Read-Text $pubFile).Trim()
  $goodSig = Join-Path $work "good.sig"
  Copy-Item -LiteralPath (Join-Path $good "checksums.txt.sig") -Destination $goodSig
  $badBytes = New-Object byte[] 64
  (New-Object Random).NextBytes($badBytes)
  $badSig = Join-Path $work "bad.sig"
  Write-Text $badSig ([Convert]::ToBase64String($badBytes) + "`n")

  $serverExe = Join-Path $work "server.exe"
  Use-Env $goEnv {
    & $goExe -C $src build -trimpath -o $serverExe ./server
    if ($LASTEXITCODE -ne 0) { throw "go build of the server failed" }
  }
  $addrFile = Join-Path $work "server.addr"
  $requests = Join-Path $work "requests.log"
  $server = Start-Process -FilePath $serverExe -PassThru -NoNewWindow `
    -ArgumentList @(('"' + $srv + '"'), ('"' + $addrFile + '"'), ('"' + $requests + '"'), $ver) `
    -RedirectStandardOutput (Join-Path $work "server.out") -RedirectStandardError (Join-Path $work "server.err")
  $deadline = (Get-Date).AddSeconds(20)
  while (-not (Test-Path -LiteralPath $addrFile)) {
    if ($server.HasExited -or (Get-Date) -gt $deadline) {
      throw ("the loopback server did not start: " + (Read-Text (Join-Path $work "server.err")))
    }
    Start-Sleep -Milliseconds 100
  }
  $origin = (Read-Text $addrFile).Trim()
  if ($origin -notmatch '^http://127\.0\.0\.1:\d+$') { throw "the server is not on loopback: $origin" }

  # The copy under test: the throwaway key instead of the release key, and the
  # server instead of github.com, each rewrite checked.
  function Set-OneLine([string]$text, [string]$pattern, [string]$line, [string]$what) {
    $rx = New-Object Text.RegularExpressions.Regex($pattern, [Text.RegularExpressions.RegexOptions]::Multiline)
    $found = $rx.Matches($text)
    if ($found.Count -ne 1) { throw "FAIL: $($found.Count) lines of install.ps1 set ${what}, want 1" }
    $m = $found[0]
    return $text.Substring(0, $m.Index) + $m.Groups[1].Value + $line + $text.Substring($m.Index + $m.Length)
  }
  $original = [IO.File]::ReadAllText((Join-Path $PSScriptRoot "install.ps1"))
  $copy = Set-OneLine $original '^([ \t]*)\$releaseKeys = [^\r\n]*' ('$releaseKeys = @("' + $pub + '")') "the release keys"
  $copy = Set-OneLine $copy '^([ \t]*)\$latestUrl = [^\r\n]*' ('$latestUrl = "' + $origin + '/$repo/releases/latest"') "the latest-release URL"
  $copy = Set-OneLine $copy '^([ \t]*)\$base = [^\r\n]*' ('$base = "' + $origin + '/$repo/releases/download/v$version"') "the download base"
  if ($copy -eq $original) { throw "FAIL: the copy was not changed" }
  foreach ($want in @(('$releaseKeys = @("' + $pub + '")'), ('$latestUrl = "' + $origin + '/$repo/releases/latest"'), ('$base = "' + $origin + '/$repo/releases/download/v$version"'))) {
    if (-not $copy.Contains($want)) { throw "FAIL: the copy does not hold: $want" }
  }
  foreach ($l in ($copy -split "`r?`n")) {
    if ($l -notmatch '^\s*#' -and $l -match 'github\.com|jevlin\.ai') { throw "FAIL: the copy still names a real host: $l" }
  }
  Write-Text (Join-Path $srv "install.ps1") $copy
  # install.cmd, which runs install.ps1 in Windows PowerShell, pointed the same way.
  $cmdOriginal = [IO.File]::ReadAllText((Join-Path $PSScriptRoot "install.cmd"))
  $realPs1 = "https://jevlin.ai/install.ps1"
  if (([regex]::Matches($cmdOriginal, [regex]::Escape($realPs1))).Count -ne 1) { throw "FAIL: install.cmd does not name $realPs1 once" }
  $cmdCopy = Join-Path $work "install.cmd"
  Write-Text $cmdCopy $cmdOriginal.Replace($realPs1, "$origin/install.ps1")
  foreach ($l in ((Read-Text $cmdCopy) -split "`r?`n")) {
    if ($l -notmatch '^\s*rem\b' -and $l -match 'github\.com|jevlin\.ai') { throw "FAIL: the install.cmd copy still names a real host: $l" }
  }

  # What each case runs, in a fresh process of this PowerShell. It sets values
  # of its own for $ErrorActionPreference and the TLS setting first, runs the
  # installer the documented way, and then says whether either changed and
  # which of the installer's names are still defined: `irm | iex` runs a script
  # in the caller's session. An error is printed whole on its own line before it
  # is rethrown, so a long one is not wrapped where a case looks for it, and the
  # process still exits 1 as an uncaught error would make it.
  $childScript = @'
$ErrorActionPreference = 'Continue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls13
$callerTls = [Net.ServicePointManager]::SecurityProtocol
Write-Host ("terminal: UserInteractive={0} IsInputRedirected={1}" -f [Environment]::UserInteractive, [Console]::IsInputRedirected)
try {
  irm 'ORIGIN/install.ps1' | iex
} catch {
  [Console]::Error.WriteLine("error: " + $_.Exception.Message)
  throw
}
$left = @(@('repo', 'arch', 'latestUrl', 'version', 'base', 'tmp', 'releaseKeys', 'exe', 'staged', 'displaced', 'leftover', 'tlsBefore') | Where-Object { Test-Path -LiteralPath "variable:$_" })
foreach ($f in @('Get-JevlinVersion', 'Remove-Staged')) { if (Get-Command $f -ErrorAction SilentlyContinue) { $left += $f } }
Write-Host ("after: ErrorActionPreference={0} TLS unchanged={1} left=[{2}]" -f $ErrorActionPreference, ([Net.ServicePointManager]::SecurityProtocol -eq $callerTls), ($left -join ','))
'@
  $childScript = $childScript.Replace('ORIGIN', $origin)
  $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($childScript))
  $noLeak = "after: ErrorActionPreference=Continue TLS unchanged=True left=[]"

  $emptyIn = Join-Path $work "stdin.empty"
  Write-Text $emptyIn ""
  $childTemp = Join-Path $work "childtemp"
  New-Item -ItemType Directory -Path $childTemp | Out-Null
  $calls = Join-Path $work "calls.log"

  # Runs the installer once: stdin from an empty file, as in an agent's shell,
  # stdout and stderr to files, and the installer's own temporary directory
  # under $childTemp, so a case can tell it was removed.
  function Invoke-Installer {
    param([string]$JevlinHome, [hashtable]$Extra = @{}, [switch]$ViaCmd)
    $vars = @{
      JEVLIN_HOME = $JevlinHome; JEVLIN_NO_SETUP = "1"; JEVLIN_VERSION = $null; JEVLIN_SKIP_SIGNATURE = $null
      JEVLIN_TEST_STUB_LOG = $calls; TEMP = $childTemp; TMP = $childTemp
    }
    foreach ($k in @($Extra.Keys)) { $vars[$k] = $Extra[$k] }
    if ($ViaCmd) {
      $file = $env:ComSpec
      $argList = @('/d', '/c', ('"' + $cmdCopy + '"'))
    } else {
      $file = $psExe
      $argList = @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-OutputFormat', 'Text', '-EncodedCommand', $encoded)
    }
    $so = Join-Path $work "case.out"
    $se = Join-Path $work "case.err"
    $sw = [Diagnostics.Stopwatch]::StartNew()
    # Start-Process -Wait would wait for every process the run started, and a
    # case's stub may leave one running on purpose, so only this one is awaited.
    Use-Env $vars {
      $p = Start-Process -FilePath $file -ArgumentList $argList -NoNewWindow -PassThru `
        -RedirectStandardInput $emptyIn -RedirectStandardOutput $so -RedirectStandardError $se
      $null = $p.Handle
      if ($p.WaitForExit(300000)) {
        $script:rc = $p.ExitCode
      } else {
        Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
        $script:rc = "timeout"
      }
    }
    $script:secs = $sw.Elapsed.TotalSeconds
    $script:out = (Read-Text $so) + (Read-Text $se)
  }

  function Get-Reported([string]$exe) {
    if (-not (Test-Path -LiteralPath $exe)) { return "(no jevlin.exe)" }
    try {
      $o = & $exe version
      $code = $LASTEXITCODE
    } catch {
      return "(did not run: $($_.Exception.Message))"
    }
    if ($code -ne 0) { return "(exit $code)" }
    return "$o".Trim()
  }

  function Get-Calls { @((Read-Text $calls) -split "`r?`n" | Where-Object { $_ }) }

  # Stops every process running from a file under $dir; returns how many.
  function Stop-RunningFrom([string]$dir) {
    $prefix = $dir.TrimEnd('\') + '\'
    $procs = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase) })
    foreach ($pr in $procs) {
      Stop-Process -Id $pr.ProcessId -Force -ErrorAction SilentlyContinue
      Wait-Process -Id $pr.ProcessId -Timeout 10 -ErrorAction SilentlyContinue
    }
    $procs.Count
  }

  # Holds the last run to what a case expects and prints ok or FAIL with the
  # run's output, as install_test.sh does.
  function Test-Install {
    param(
      [string]$Label,
      [string]$JevlinHome,
      [switch]$WantFailure,
      [string[]]$Says = @(),
      [string[]]$DoesNotSay = @(),
      [string]$Installed = "",
      [int]$Displaced = 0,
      [int]$Staged = 0,
      [string[]]$More = @()
    )
    $problems = New-Object Collections.Generic.List[string]
    if ("$($script:rc)" -eq "timeout") {
      $problems.Add("the installer did not finish in 5 minutes and was stopped")
    } elseif ($WantFailure) {
      if ($script:rc -eq 0) { $problems.Add("exit 0, want non-zero") }
    } elseif ($script:rc -ne 0) {
      $problems.Add("exit $($script:rc), want 0")
    }
    foreach ($s in $Says) { if (-not $script:out.Contains($s)) { $problems.Add("does not say: $s") } }
    foreach ($s in $DoesNotSay) { if ($script:out.Contains($s)) { $problems.Add("says: $s") } }
    $bin = Join-Path $JevlinHome "bin"
    $exe = Join-Path $bin "jevlin.exe"
    if ($Installed) {
      $got = Get-Reported $exe
      if ($got -ne "jevlin $Installed") { $problems.Add("jevlin.exe version says '$got', want 'jevlin $Installed'") }
    } elseif (Test-Path -LiteralPath $exe) {
      $problems.Add("jevlin.exe was installed")
    }
    if (Test-Path -LiteralPath $bin) {
      $new = @(Get-ChildItem -LiteralPath $bin -Filter 'jevlin.new-*')
      if ($new.Count -ne $Staged) { $problems.Add("$($new.Count) staged copies in bin, want ${Staged}: $(($new | ForEach-Object { $_.Name }) -join ', ')") }
      $aside = @(Get-ChildItem -LiteralPath $bin -Filter 'jevlin.exe.displaced*')
      if ($aside.Count -ne $Displaced) { $problems.Add("$($aside.Count) displaced copies in bin, want $Displaced") }
    }
    $tmpLeft = @(Get-ChildItem -LiteralPath $childTemp -Filter 'jevlin-install-*')
    if ($tmpLeft.Count -ne 0) {
      $problems.Add("the installer left its temporary directory")
      $tmpLeft | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
    }
    foreach ($m in $More) { if ($m) { $problems.Add($m) } }
    $script:cases++
    $took = "{0:N1} s" -f $script:secs
    if ($problems.Count -eq 0) {
      Write-Host "ok   $Label [$took]"
    } else {
      Write-Host "FAIL $Label [$took]"
      foreach ($p in $problems) { Write-Host "       - $p" }
      $script:fail = 1
    }
    # Windows PowerShell writes its progress records to a redirected stderr as
    # CLIXML even with -OutputFormat Text; they are left out of what is shown,
    # never out of what a case reads.
    foreach ($l in ($script:out -split "`r?`n")) {
      if ($l -and $l -notmatch '^#< CLIXML$|<Objs |<Obj S=|</Obj>|RefId=|</Objs>') { Write-Host "       | $l" }
    }
  }

  $h1 = Join-Path $work "h1"
  $exe1 = Join-Path $h1 "bin\jevlin.exe"

  Invoke-Installer $h1
  $asked = Read-Text $requests
  $more = @()
  if (-not $asked.Contains("GET /jevlinai/jevlin-go/releases/latest")) { $more += "the latest-release URL was never asked" }
  if ($asked.Contains("/releases/tag/")) { $more += "the redirect to the tag was followed; it should only be read" }
  Test-Install "(w1) latest through the 302, no pin: verified, installed, version runs, nothing left in the session" $h1 `
    -Says @("release signature verified (node)", "installed $exe1 (jevlin $ver)", "next: $exe1 setup", $noLeak) -Installed $ver -More $more

  Invoke-Installer $h1
  Test-Install "(w2) reinstall over w1: installed, no displaced copy left" $h1 `
    -Says @("installed $exe1 (jevlin $ver)", $noLeak) -DoesNotSay @("could not be removed") -Installed $ver

  # The installed jevlin.exe still running, as an agent's session may hold it:
  # it can be moved aside but not deleted, so the displaced copy stays and the
  # installer says where. A second install while that copy is still held takes
  # a name of its own, where a fixed name would be in the way.
  # When w1 and w2 installed nothing there is no jevlin.exe to keep running:
  # both cases fail, and the run goes on to the cases that do not need one.
  if (Test-Path -LiteralPath $exe1) {
    $holder = Start-Process -FilePath $exe1 -ArgumentList "hold" -PassThru -NoNewWindow
    Start-Sleep -Milliseconds 500
    if ($holder.HasExited) { throw "the stub started with hold exited at once (exit $($holder.ExitCode))" }
    Invoke-Installer $h1
    Test-Install "(w2b) reinstall while jevlin.exe runs: installed, the held copy is named and left" $h1 `
      -Says @("installed $exe1 (jevlin $ver)", "the previous jevlin.exe could not be removed, most likely because it is still running: delete $exe1.displaced-") `
      -Installed $ver -Displaced 1
    Invoke-Installer $h1
    Test-Install "(w2c) again while that copy is still held: a fresh name, installed" $h1 `
      -Says @("installed $exe1 (jevlin $ver)") -DoesNotSay @("could not be removed") -Installed $ver -Displaced 1
    Stop-Process -Id $holder.Id -Force
    $holder.WaitForExit()
    $holder = $null
    Get-ChildItem -LiteralPath (Join-Path $h1 "bin") -Filter 'jevlin.exe.displaced*' | Remove-Item -Force
  } else {
    foreach ($c in @("(w2b) reinstall while jevlin.exe runs", "(w2c) again while that copy is still held")) {
      Write-Host "FAIL ${c}: not run, as w1 installed no jevlin.exe to keep running"
      $cases++
      $fail = 1
    }
  }

  # The replace step (change 2): a new binary that does not run never replaces
  # one that does.
  [IO.File]::WriteAllText($calls, "")
  Invoke-Installer $h1 @{ JEVLIN_VERSION = "9.9.10" }
  $more = @()
  if (-not (@(Get-Calls) -match '^9\.9\.10 jevlin\.new-[0-9a-f]+\.exe version$')) { $more += "the new binary was never run under its staged name" }
  if (@(Get-Calls) -match '^9\.9\.10 jevlin\.exe ') { $more += "the new binary was run as jevlin.exe" }
  Test-Install "(w5) the new jevlin.exe does not run: refused before the swap, the installed one kept and runs" $h1 -WantFailure `
    -Says @("jevlin install: the downloaded jevlin.exe does not run here (", "$exe1 was left as it was") -Installed $ver -More $more

  [IO.File]::WriteAllText($calls, "")
  Invoke-Installer $h1 @{ JEVLIN_VERSION = "9.9.11" }
  $more = @()
  if (-not (@(Get-Calls) -match '^9\.9\.11 jevlin\.exe version$')) { $more += "the new binary was never run from jevlin.exe" }
  Test-Install "(w5b) it runs staged but not as jevlin.exe: the previous jevlin.exe is put back" $h1 -WantFailure `
    -Says @("jevlin install: the new jevlin.exe does not run from $exe1 (", "the previous jevlin.exe was put back") -Installed $ver -More $more

  # The failed binary started a process of its own from its staged file before
  # it exited, so the installer cannot remove that file: it must name it.
  [IO.File]::WriteAllText($calls, "")
  Invoke-Installer $h1 @{ JEVLIN_VERSION = "9.9.12" }
  $bin1 = Join-Path $h1 "bin"
  $stagedLeft = @(Get-ChildItem -LiteralPath $bin1 -Filter 'jevlin.new-*')
  $named = if ($stagedLeft.Count -eq 1) { $stagedLeft[0].FullName } else { "(not one staged file but $($stagedLeft.Count))" }
  $more = @()
  $deadline = (Get-Date).AddSeconds(10)
  while (-not (@(Get-Calls) -match '^9\.9\.12 jevlin\.new-[0-9a-f]+\.exe hold$') -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 200 }
  if (-not (@(Get-Calls) -match '^9\.9\.12 jevlin\.new-[0-9a-f]+\.exe hold$')) { $more += "the stub's own process never started from the staged file; the case proves nothing" }
  Test-Install "(w5c) the failed new jevlin.exe is still running and cannot be removed: it is named" $h1 -WantFailure `
    -Says @("jevlin install: the downloaded jevlin.exe does not run here (", "$exe1 was left as it was; $named could not be removed, most likely because a process still runs it: delete it once that exits") `
    -Installed $ver -Staged 1 -More $more
  $stopped = Stop-RunningFrom $bin1
  if ($stopped -ne 1) { Write-Host "FAIL (w5c): $stopped processes were running from bin, want the one the stub started"; $fail = 1 }
  $stagedLeft | Remove-Item -Force

  # No node: every directory on PATH that holds one is dropped, and the drop is
  # checked with the same lookup the installer makes.
  $noNodePath = (($env:PATH -split ';') | Where-Object {
      $dir = $_
      if (-not $dir) { return $false }
      $hasNode = $false
      foreach ($n in @('node.exe', 'node.cmd', 'node.bat', 'node.ps1', 'node.com')) {
        try { if (Test-Path -LiteralPath (Join-Path $dir $n)) { $hasNode = $true } } catch { }
      }
      -not $hasNode
    }) -join ';'
  $stillNode = Use-Env @{ PATH = $noNodePath } { Get-Command node -ErrorAction SilentlyContinue }
  $h3 = Join-Path $work "h3"
  Invoke-Installer $h3 @{ PATH = $noNodePath }
  $more = @()
  if ($stillNode) { $more += "node is still on the PATH meant to have none: $($stillNode.Source)" }
  Test-Install "(w3) node hidden from PATH: the checksum alone, installed" $h3 `
    -Says @("release signature not checked (no verifier on this machine: node); relying on the checksum alone") `
    -DoesNotSay @("release signature verified") -Installed $ver -More $more

  Copy-Item -LiteralPath $badSig -Destination (Join-Path $good "checksums.txt.sig") -Force
  $h4 = Join-Path $work "h4"
  Invoke-Installer $h4
  Test-Install "(w4) bad signature, node present: refused, nothing installed" $h4 -WantFailure `
    -Says @("checksums.txt.sig does not verify against the project's release key; nothing was installed")
  Copy-Item -LiteralPath $goodSig -Destination (Join-Path $good "checksums.txt.sig") -Force

  # The one run without JEVLIN_NO_SETUP. Its stdin is a file, as in an agent's
  # shell, so the installer must print the command to run, exit 0, and never
  # start setup. The binary is the stub, so a setup it did start would only be
  # logged.
  [IO.File]::WriteAllText($calls, "")
  $h6 = Join-Path $work "h6"
  $exe6 = Join-Path $h6 "bin\jevlin.exe"
  Invoke-Installer $h6 @{ JEVLIN_NO_SETUP = $null }
  $more = @()
  if (@(Get-Calls) -match ' setup$') { $more += "the installer ran setup with no terminal" }
  Test-Install "(w6) no terminal, no JEVLIN_NO_SETUP: prints the setup command, exit 0" $h6 `
    -Says @("no terminal attached; run: $exe6 setup") -Installed $ver -More $more

  $h7 = Join-Path $work "h7"
  $exe7 = Join-Path $h7 "bin\jevlin.exe"
  Invoke-Installer $h7 -ViaCmd
  Test-Install "(w7) install.cmd: Windows PowerShell runs install.ps1, latest, verified" $h7 `
    -Says @("release signature verified (node)", "installed $exe7 (jevlin $ver)") -Installed $ver
} finally {
  if ($holder -and -not $holder.HasExited) { Stop-Process -Id $holder.Id -Force -ErrorAction SilentlyContinue }
  if (Get-Command Stop-RunningFrom -ErrorAction SilentlyContinue) { $null = Stop-RunningFrom $work }
  if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue }
}

if ($fail -eq 0) {
  Write-Host "$cases cases, all ok"
  Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
} else {
  Write-Host "$cases cases, some failed; the work directory is kept: $work"
}
exit $fail
