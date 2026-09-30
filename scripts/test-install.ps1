#Requires -Version 5.1
<#
.SYNOPSIS
    Pure-PowerShell sandbox test for scripts/install-agent.ps1 (no Pester).

.DESCRIPTION
    Runs install-agent.ps1 against a sandbox root (-InstallRoot) with a
    fake sc.exe shim (CP_SC) and a local HTTP asset server, asserting on
    the resulting files/ACL calls/service-shim invocations. Intended to
    run on windows-latest CI (the sc.exe shim and real icacls are both
    available there); can also run on a local Windows dev machine with
    `pwsh -File scripts/test-install.ps1` (PowerShell 7+) or
    `powershell -File scripts/test-install.ps1` (Windows PowerShell 5.1).

.NOTES
    This script builds no Go binaries itself — it expects a
    cloud-pulse-agent-windows-<arch>.exe + checksums.txt to already
    exist in a directory it serves over a local HTTP listener, so it can
    run standalone against any prebuilt asset directory via
    -AssetDir, or (the CI default) against assets this script builds
    itself via `go build` if -AssetDir is not given and `go` is on PATH.
#>
param(
    [string]$AssetDir
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Script:PassCount = 0
$Script:FailCount = 0

function Write-Pass {
    param([string]$Message)
    Write-Host "PASS: $Message"
    $Script:PassCount++
}

function Write-Fail {
    param([string]$Message)
    Write-Host "FAIL: $Message" -ForegroundColor Red
    $Script:FailCount++
}

function Assert-FileExists {
    param([string]$Description, [string]$Path)
    if (Test-Path $Path) { Write-Pass $Description } else { Write-Fail "$Description (missing: $Path)" }
}

function Assert-FileAbsent {
    param([string]$Description, [string]$Path)
    if (-not (Test-Path $Path)) { Write-Pass $Description } else { Write-Fail "$Description (should be absent: $Path)" }
}

function Assert-FileContains {
    param([string]$Description, [string]$Path, [string]$Substring)
    if ((Test-Path $Path) -and (Select-String -Path $Path -Pattern ([regex]::Escape($Substring)) -Quiet)) {
        Write-Pass $Description
    }
    else {
        Write-Fail "$Description (expected '$Substring' in $Path)"
    }
}

$RepoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $RepoRoot
try {
    $tmpRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("cloud-pulse-test-install-ps." + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmpRoot -Force | Out-Null

    try {
        # -----------------------------------------------------------------
        # Build (or reuse) a windows/amd64 agent asset + checksums.txt
        # -----------------------------------------------------------------
        $distDir = Join-Path $tmpRoot 'dist'
        if ($AssetDir) {
            $distDir = $AssetDir
        }
        else {
            New-Item -ItemType Directory -Path $distDir -Force | Out-Null
            $goCmd = Get-Command go -ErrorAction SilentlyContinue
            if (-not $goCmd) {
                Write-Host 'SKIP: go is not on PATH and -AssetDir was not given; cannot build a test asset'
                Write-Host '===================================================='
                Write-Host "test-install.ps1 results: $Script:PassCount passed, $Script:FailCount failed (SKIPPED)"
                Write-Host '===================================================='
                exit 0
            }
            $env:TARGETS = 'windows/amd64'
            $env:ALLOW_ANY_VERSION = '1'
            $buildScript = Join-Path $RepoRoot 'scripts' 'build-release.sh'
            $bashCmd = Get-Command bash -ErrorAction SilentlyContinue
            if ($bashCmd) {
                & bash $buildScript 'v0.0.0-test' $distDir
            }
            else {
                # No bash available (a bare Windows runner without Git
                # Bash/WSL) — build directly via `go build` instead of
                # calling the shared shell script, matching what
                # build-release.sh itself does for a single target.
                $exePath = Join-Path $distDir 'cloud-pulse-agent-windows-amd64.exe'
                $env:GOOS = 'windows'
                $env:GOARCH = 'amd64'
                $env:CGO_ENABLED = '0'
                & go build -o $exePath ./cmd/agent
                $hash = (Get-FileHash -Path $exePath -Algorithm SHA256).Hash.ToLowerInvariant()
                "$hash  cloud-pulse-agent-windows-amd64.exe" | Set-Content -Path (Join-Path $distDir 'checksums.txt')
            }
        }
        Assert-FileExists 'build produced windows agent asset' (Join-Path $distDir 'cloud-pulse-agent-windows-amd64.exe')
        Assert-FileExists 'build produced checksums.txt' (Join-Path $distDir 'checksums.txt')

        # -----------------------------------------------------------------
        # Local HTTP asset server (background job, its own HttpListener)
        # -----------------------------------------------------------------
        $port = 18401
        $serverJob = Start-Job -ScriptBlock {
            param($DistDir, $Port)
            $l = New-Object System.Net.HttpListener
            $l.Prefixes.Add("http://127.0.0.1:$Port/")
            $l.Start()
            while ($l.IsListening) {
                try {
                    $ctx = $l.GetContext()
                    $reqPath = $ctx.Request.Url.LocalPath.TrimStart('/')
                    $filePath = Join-Path $DistDir $reqPath
                    if (Test-Path $filePath) {
                        $bytes = [System.IO.File]::ReadAllBytes($filePath)
                        $ctx.Response.ContentLength64 = $bytes.Length
                        $ctx.Response.OutputStream.Write($bytes, 0, $bytes.Length)
                    }
                    else {
                        $ctx.Response.StatusCode = 404
                    }
                    $ctx.Response.OutputStream.Close()
                }
                catch {
                    break
                }
            }
        } -ArgumentList $distDir, $port
        Start-Sleep -Seconds 1
        $baseUrl = "http://127.0.0.1:$port"

        # -----------------------------------------------------------------
        # Fake sc.exe shim: logs every invocation, tracks minimal state
        # -----------------------------------------------------------------
        $scLog = Join-Path $tmpRoot 'sc.log'
        $scState = Join-Path $tmpRoot 'sc-state'
        New-Item -ItemType Directory -Path $scState -Force | Out-Null
        $scShimPath = Join-Path $tmpRoot 'fake-sc.ps1'
        @'
param([Parameter(ValueFromRemainingArguments = $true)][string[]]$ScArgs)
$logPath = $env:CP_TEST_SC_LOG
$statePath = $env:CP_TEST_SC_STATE
Add-Content -Path $logPath -Value ($ScArgs -join ' ')
switch ($ScArgs[0]) {
    'create' {
        New-Item -ItemType File -Path (Join-Path $statePath $ScArgs[1]) -Force | Out-Null
        exit 0
    }
    'query' {
        if (Test-Path (Join-Path $statePath $ScArgs[1])) { exit 0 } else { exit 1 }
    }
    'delete' {
        Remove-Item -Path (Join-Path $statePath $ScArgs[1]) -Force -ErrorAction SilentlyContinue
        exit 0
    }
    'start' { exit 0 }
    'stop' { exit 0 }
    'failure' { exit 0 }
    default { exit 0 }
}
'@ | Set-Content -Path $scShimPath

        $env:CP_TEST_SC_LOG = $scLog
        $env:CP_TEST_SC_STATE = $scState
        New-Item -ItemType File -Path $scLog -Force | Out-Null

        $sandbox = Join-Path $tmpRoot 'sandbox'
        New-Item -ItemType Directory -Path $sandbox -Force | Out-Null

        $installScript = Join-Path $RepoRoot 'scripts' 'install-agent.ps1'

        # Load installer functions without invoking Main, then exercise the
        # real (non-sandbox) path branch. This caught the former Join-Path ''
        # failure that made every normal Windows install abort before any
        # service/ACL work began.
        $previousNoMain = [Environment]::GetEnvironmentVariable('CP_INSTALL_AGENT_NO_MAIN', 'Process')
        $previousInstallRoot = [Environment]::GetEnvironmentVariable('CP_INSTALL_ROOT', 'Process')
        try {
            [Environment]::SetEnvironmentVariable('CP_INSTALL_AGENT_NO_MAIN', '1', 'Process')
            [Environment]::SetEnvironmentVariable('CP_INSTALL_ROOT', $null, 'Process')
            . $installScript
            $realPaths = Get-InstallPaths
            $programFilesRoot = if ($env:ProgramFiles) { $env:ProgramFiles } else { Join-Path ($env:SystemDrive + '\') 'Program Files' }
            $programDataRoot = if ($env:ProgramData) { $env:ProgramData } else { Join-Path ($env:SystemDrive + '\') 'ProgramData' }
            $allPaths = @($realPaths.ProgramDir, $realPaths.BinPath, $realPaths.DataDir, $realPaths.EnvFile, $realPaths.RequestDir, $realPaths.ResultDir)
            if ($allPaths | Where-Object { [string]::IsNullOrWhiteSpace($_) -or -not [System.IO.Path]::IsPathRooted($_) }) {
                throw 'Get-InstallPaths returned an empty or relative real-mode path'
            }
            if (-not $realPaths.ProgramDir.StartsWith($programFilesRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
                -not $realPaths.DataDir.StartsWith($programDataRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
                -not $realPaths.EnvFile.StartsWith($programDataRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
                -not $realPaths.RequestDir.StartsWith($programDataRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
                -not $realPaths.ResultDir.StartsWith($programDataRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
                throw 'Get-InstallPaths returned a real-mode path outside Program Files or ProgramData'
            }
            Write-Pass 'Get-InstallPaths loads without Main and returns non-empty absolute real-mode paths'
        }
        catch {
            Write-Fail "Get-InstallPaths real-mode path regression: $_"
        }
        finally {
            [Environment]::SetEnvironmentVariable('CP_INSTALL_AGENT_NO_MAIN', $previousNoMain, 'Process')
            [Environment]::SetEnvironmentVariable('CP_INSTALL_ROOT', $previousInstallRoot, 'Process')
        }

        $env:CP_RELEASE_BASE_URL = $baseUrl
        $env:CP_SC = $scShimPath
        $env:CP_TEST_UNAME_M = 'amd64'

        # -----------------------------------------------------------------
        # Fresh install
        # -----------------------------------------------------------------
        $tokenFile = Join-Path $tmpRoot 'token.txt'
        'a-fake-agent-token-not-a-real-secret-0123456789' | Set-Content -Path $tokenFile -NoNewline

        & pwsh -NoProfile -File $installScript -Install -Yes `
            -HubUrl "http://127.0.0.1:$port" -TokenFile $tokenFile -InstallRoot $sandbox 2>&1 |
            Tee-Object -Variable installOutput | Out-Null
        if ($LASTEXITCODE -eq 0) {
            Write-Pass 'install-agent.ps1 -Install sandbox exited 0'
        }
        else {
            Write-Fail "install-agent.ps1 -Install sandbox exited $LASTEXITCODE"
            $installOutput | ForEach-Object { Write-Host "  $_" }
        }

        $binPath = Join-Path $sandbox 'Program Files\cloud-pulse\cloud-pulse-agent.exe'
        $envFile = Join-Path $sandbox 'ProgramData\cloud-pulse\agent.env'
        Assert-FileExists 'agent binary installed' $binPath
        Assert-FileExists 'agent.env created' $envFile
        Assert-FileContains 'agent.env contains CP_HUB_URL' $envFile "CP_HUB_URL=http://127.0.0.1:$port"
        Assert-FileContains 'agent.env contains CP_AGENT_TOKEN' $envFile 'CP_AGENT_TOKEN=a-fake-agent-token-not-a-real-secret-0123456789'

        Assert-FileContains 'sc.exe shim was invoked with create' $scLog 'create cloud-pulse-agent'
        Assert-FileContains 'sc.exe shim was invoked with start' $scLog 'start cloud-pulse-agent'

        # -----------------------------------------------------------------
        # Reinstall (upgrade) — should restart, not recreate
        # -----------------------------------------------------------------
        Clear-Content -Path $scLog
        & pwsh -NoProfile -File $installScript -Reinstall -Yes -InstallRoot $sandbox 2>&1 |
            Tee-Object -Variable reinstallOutput | Out-Null
        if ($LASTEXITCODE -eq 0) {
            Write-Pass 'install-agent.ps1 -Reinstall sandbox exited 0'
        }
        else {
            Write-Fail "install-agent.ps1 -Reinstall sandbox exited $LASTEXITCODE"
            $reinstallOutput | ForEach-Object { Write-Host "  $_" }
        }
        Assert-FileContains 'reinstall stopped the service before restart' $scLog 'stop cloud-pulse-agent'
        Assert-FileContains 'reinstall started the service again' $scLog 'start cloud-pulse-agent'

        # -----------------------------------------------------------------
        # Uninstall + purge
        # -----------------------------------------------------------------
        & pwsh -NoProfile -File $installScript -Uninstall -Purge -InstallRoot $sandbox 2>&1 |
            Tee-Object -Variable uninstallOutput | Out-Null
        if ($LASTEXITCODE -eq 0) {
            Write-Pass 'install-agent.ps1 -Uninstall -Purge exited 0'
        }
        else {
            Write-Fail "install-agent.ps1 -Uninstall -Purge exited $LASTEXITCODE"
            $uninstallOutput | ForEach-Object { Write-Host "  $_" }
        }
        Assert-FileAbsent 'uninstall removed the binary' $binPath
        Assert-FileAbsent 'purge removed agent.env' $envFile
        Assert-FileContains 'uninstall issued sc.exe delete' $scLog 'delete cloud-pulse-agent'
    }
    finally {
        if ($serverJob) {
            Stop-Job -Job $serverJob -ErrorAction SilentlyContinue
            Remove-Job -Job $serverJob -Force -ErrorAction SilentlyContinue
        }
        Remove-Item -Path $tmpRoot -Recurse -Force -ErrorAction SilentlyContinue
    }

    Write-Host ''
    Write-Host '===================================================='
    Write-Host "test-install.ps1 results: $Script:PassCount passed, $Script:FailCount failed"
    Write-Host '===================================================='
    if ($Script:FailCount -gt 0) { exit 1 }
    exit 0
}
finally {
    Pop-Location
}
