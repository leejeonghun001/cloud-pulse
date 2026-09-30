#Requires -Version 5.1
<#
.SYNOPSIS
    One-click installer for cloud-pulse-agent on Windows.

.DESCRIPTION
    Downloads a pre-compiled cloud-pulse-agent release binary from GitHub
    Releases, verifies its sha256 checksum against checksums.txt, installs
    it under Program Files, writes its configuration to ProgramData, and
    registers it as a Windows service running under the
    "NT SERVICE\cloud-pulse-agent" virtual account (SPEC-v0.7 §1).

    Mirrors scripts/install-agent.sh's flag/menu/sandbox semantics as
    closely as PowerShell conventions allow: run with no parameters at an
    interactive console to see a menu (Install/Reinstall/Uninstall/Exit);
    pass any parameter to skip the menu and run non-interactively.

.PARAMETER Install
    Fresh install. Fails if already installed.

.PARAMETER Reinstall
    Upgrade/reinstall an existing install in place, keeping all
    configuration. Fails if not installed.

.PARAMETER Uninstall
    Stop/remove the services and binary. Config is kept unless -Purge.

.PARAMETER Purge
    With -Uninstall, also remove the configuration file and state
    directories.

.PARAMETER Yes
    Assume default confirmation answers; never blocks on a prompt.

.PARAMETER HubUrl
    cloud-pulse hub base URL. Required on first install; reused on
    upgrade unless given again.

.PARAMETER Token
    CP_AGENT_TOKEN. Mutually exclusive with -TokenFile. If neither is
    given interactively, prompts via Read-Host -AsSecureString.

.PARAMETER TokenFile
    Path to a file containing CP_AGENT_TOKEN on its first line. Mutually
    exclusive with -Token. Never put the token directly on the command
    line in a shared/logged shell history — prefer this or the
    interactive prompt.

.PARAMETER HostId
    Sets CP_HOST_ID (default: system hostname).

.PARAMETER RemoteUpdate
    Opt this agent into the hub's remote batch-update feature
    (CP_REMOTE_UPDATE=on) and install the cloud-pulse-agent-updater
    service (SPEC-v0.7 §1).

.PARAMETER Version
    Install a specific release tag instead of latest (allows downgrade).

.PARAMETER InstallRoot
    Sandbox mode: prefix every system path this script touches with this
    directory instead of the real Program Files/ProgramData. Service
    registration is skipped and replaced with "sandbox: would ..."
    messages unless $env:CP_SC names an sc.exe-compatible shim.

.EXAMPLE
    & ([scriptblock]::Create((irm https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.ps1))) -Install -HubUrl http://100.x.y.z:8090 -Token TOKEN -Yes

.NOTES
    Environment overrides (mirroring install-agent.sh):
      CP_RELEASE_BASE_URL   Override the asset download base URL.
      CP_SC                 Override the sc.exe-equivalent binary path
                             (test hook; a shim implementing
                             create/delete/start/stop/query).
      CP_TEST_UNAME_M       Override the detected architecture
                             ("amd64"|"arm64") for testing.
#>
[CmdletBinding(DefaultParameterSetName = 'Default')]
param(
    [switch]$Install,
    [switch]$Reinstall,
    [switch]$Uninstall,
    [switch]$Purge,
    [switch]$Yes,
    [string]$HubUrl,
    [string]$Token,
    [string]$TokenFile,
    [string]$HostId,
    [switch]$RemoteUpdate,
    [string]$Version,
    [string]$InstallRoot = $env:CP_INSTALL_ROOT
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

$CpRepo = 'leejeonghun001/cloud-pulse'
$CpGithubBaseUrl = "https://github.com/$CpRepo"
$CpAssetPrefix = 'cloud-pulse-agent'
$CpServiceName = 'cloud-pulse-agent'
$CpUpdaterServiceName = 'cloud-pulse-agent-updater'

$ScBinary = if ($env:CP_SC) { $env:CP_SC } else { 'sc.exe' }
$SandboxRoot = $InstallRoot
$IsSandbox = [bool]$SandboxRoot

# ---------------------------------------------------------------------------
# Path setup (sandbox-aware) — SPEC-v0.7 §1's fixed Windows path table.
# ---------------------------------------------------------------------------

function Get-InstallPaths {
    $root = if ($SandboxRoot) { $SandboxRoot } else { '' }
    [PSCustomObject]@{
        ProgramDir = Join-Path $root 'Program Files\cloud-pulse'
        BinPath    = Join-Path $root 'Program Files\cloud-pulse\cloud-pulse-agent.exe'
        DataDir    = Join-Path $root 'ProgramData\cloud-pulse'
        EnvFile    = Join-Path $root 'ProgramData\cloud-pulse\agent.env'
        RequestDir = Join-Path $root 'ProgramData\cloud-pulse-agent'
        ResultDir  = Join-Path $root 'ProgramData\cloud-pulse-agent-update'
    }
}

# ---------------------------------------------------------------------------
# Logging helpers
# ---------------------------------------------------------------------------

function Write-Log {
    param([string]$Message)
    Write-Host "[install-agent] $Message"
}

function Write-ErrLog {
    param([string]$Message)
    Write-Host "[install-agent] error: $Message" -ForegroundColor Red
}

# ---------------------------------------------------------------------------
# Architecture / release asset detection
# ---------------------------------------------------------------------------

function Get-AgentArch {
    if ($env:CP_TEST_UNAME_M) { return $env:CP_TEST_UNAME_M }
    switch ([System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture) {
        'X64' { return 'amd64' }
        'Arm64' { return 'arm64' }
        default {
            throw "unsupported CPU architecture: $([System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture)"
        }
    }
}

function Get-ReleaseBaseUrl {
    if ($env:CP_RELEASE_BASE_URL) { return $env:CP_RELEASE_BASE_URL }
    if ($Version) { return "$CpGithubBaseUrl/releases/download/$Version" }
    return "$CpGithubBaseUrl/releases/latest/download"
}

# ---------------------------------------------------------------------------
# Download + checksum verification
# ---------------------------------------------------------------------------

function Get-AssetName {
    param([string]$Arch)
    return "$CpAssetPrefix-windows-$Arch.exe"
}

function Invoke-DownloadAndVerify {
    param([string]$TempDir, [string]$AssetName)

    $baseUrl = Get-ReleaseBaseUrl
    $assetUrl = "$baseUrl/$AssetName"
    $checksumsUrl = "$baseUrl/checksums.txt"
    $assetPath = Join-Path $TempDir $AssetName
    $checksumsPath = Join-Path $TempDir 'checksums.txt'

    Write-Log "downloading $AssetName from $baseUrl"
    Invoke-WebRequest -Uri $assetUrl -OutFile $assetPath -UseBasicParsing
    Invoke-WebRequest -Uri $checksumsUrl -OutFile $checksumsPath -UseBasicParsing

    $checksumLine = Select-String -Path $checksumsPath -Pattern ([regex]::Escape($AssetName)) |
        Select-Object -First 1
    if (-not $checksumLine) {
        throw "no checksum entry found for $AssetName in checksums.txt"
    }
    $expected = ($checksumLine.Line -split '\s+')[0]
    $actual = (Get-FileHash -Path $assetPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($expected -ne $actual) {
        throw "checksum mismatch for $AssetName`: expected $expected, got $actual"
    }
    Write-Log "checksum verified for $AssetName"
    return $assetPath
}

# ---------------------------------------------------------------------------
# Token resolution: -Token, -TokenFile, or interactive secure prompt.
# ---------------------------------------------------------------------------

function Resolve-AgentToken {
    if ($Token -and $TokenFile) {
        throw '-Token and -TokenFile are mutually exclusive'
    }
    if ($Token) { return $Token }
    if ($TokenFile) {
        $line = Get-Content -Path $TokenFile -TotalCount 1
        if (-not $line) { throw "-TokenFile $TokenFile is empty" }
        return $line.Trim()
    }
    if (-not [Environment]::UserInteractive -or $Yes) {
        throw 'CP_AGENT_TOKEN is required: pass -Token or -TokenFile (no interactive prompt available)'
    }
    $secure = Read-Host -Prompt 'Agent token (input hidden)' -AsSecureString
    $bstr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try {
        return [System.Runtime.InteropServices.Marshal]::PtrToStringAuto($bstr)
    }
    finally {
        [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
    }
}

# ---------------------------------------------------------------------------
# agent.env rendering
# ---------------------------------------------------------------------------

function Write-AgentEnvFile {
    param(
        [string]$EnvFilePath,
        [string]$ResolvedHubUrl,
        [string]$ResolvedToken,
        [string]$ResolvedHostId,
        [bool]$ResolvedRemoteUpdate
    )

    $lines = @(
        '# cloud-pulse-agent configuration. Managed by install-agent.ps1.'
        "CP_HUB_URL=$ResolvedHubUrl"
        "CP_AGENT_TOKEN=$ResolvedToken"
    )
    if ($ResolvedHostId) {
        $lines += "CP_HOST_ID=$ResolvedHostId"
    }
    $lines += "CP_REMOTE_UPDATE=$(if ($ResolvedRemoteUpdate) { 'on' } else { 'off' })"

    $dir = Split-Path -Parent $EnvFilePath
    if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
    # KEY=VALUE, no BOM, LF line endings (matches config.ParseEnvFile's
    # tolerant-but-canonical expectations; CRLF is also accepted by the
    # parser, but LF keeps this file identical in spirit to the
    # Linux/macOS env files).
    [System.IO.File]::WriteAllText($EnvFilePath, ($lines -join "`n") + "`n")
    Write-Log "wrote $EnvFilePath"
}

# ---------------------------------------------------------------------------
# ACL setup (icacls invoked with fixed argv, never a shell string)
# ---------------------------------------------------------------------------

function Set-CloudPulseAcl {
    param([string]$Path, [string]$ServiceSid, [string]$ServiceRights)

    if ($IsSandbox -and -not $env:CP_ICACLS) {
        Write-Log "sandbox: would run icacls `"$Path`" /inheritance:r /grant:r `"SYSTEM:(OI)(CI)F`" `"BUILTIN\Administrators:(OI)(CI)F`" /grant:r `"$ServiceSid`:(OI)(CI)$ServiceRights`""
        return
    }
    $icacls = if ($env:CP_ICACLS) { $env:CP_ICACLS } else { 'icacls.exe' }
    & $icacls $Path /inheritance:r | Out-Null
    & $icacls $Path /grant:r 'SYSTEM:(OI)(CI)F' | Out-Null
    & $icacls $Path /grant:r 'BUILTIN\Administrators:(OI)(CI)F' | Out-Null
    & $icacls $Path /grant:r "${ServiceSid}:(OI)(CI)$ServiceRights" | Out-Null
}

# ---------------------------------------------------------------------------
# Service control (sc.exe-based; CP_SC test hook)
# ---------------------------------------------------------------------------

function Test-ServiceInstalled {
    param([string]$Name)
    if ($IsSandbox -and -not $env:CP_SC) { return $false }
    & $ScBinary query $Name > $null 2>&1
    return $LASTEXITCODE -eq 0
}

function Install-CloudPulseService {
    param([string]$Name, [string]$DisplayName, [string]$BinaryPathWithArgs, [string]$StartAccount)

    if ($IsSandbox -and -not $env:CP_SC) {
        Write-Log "sandbox: would run sc.exe create $Name binPath= `"$BinaryPathWithArgs`" obj= `"$StartAccount`" start= auto"
        return
    }
    & $ScBinary create $Name binPath= "$BinaryPathWithArgs" obj= "$StartAccount" start= auto DisplayName= "$DisplayName" | Out-Null
    & $ScBinary failure $Name reset= 3600 actions= restart/5000 | Out-Null
    Write-Log "registered service $Name"
}

function Start-CloudPulseService {
    param([string]$Name)
    if ($IsSandbox -and -not $env:CP_SC) {
        Write-Log "sandbox: would run sc.exe start $Name"
        return
    }
    & $ScBinary start $Name | Out-Null
}

function Stop-CloudPulseService {
    param([string]$Name)
    if ($IsSandbox -and -not $env:CP_SC) {
        Write-Log "sandbox: would run sc.exe stop $Name"
        return
    }
    & $ScBinary stop $Name > $null 2>&1
}

function Uninstall-CloudPulseService {
    param([string]$Name)
    if ($IsSandbox -and -not $env:CP_SC) {
        Write-Log "sandbox: would run sc.exe delete $Name"
        return
    }
    Stop-CloudPulseService -Name $Name
    & $ScBinary delete $Name > $null 2>&1
}

# ---------------------------------------------------------------------------
# Install / uninstall flows
# ---------------------------------------------------------------------------

function Invoke-InstallFlow {
    $paths = Get-InstallPaths
    $tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("cloud-pulse-agent-install-" + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tempDir -Force | Out-Null
    try {
        $arch = Get-AgentArch
        $assetName = Get-AssetName -Arch $arch
        $assetPath = Invoke-DownloadAndVerify -TempDir $tempDir -AssetName $assetName

        if (-not (Test-Path $paths.ProgramDir)) {
            New-Item -ItemType Directory -Path $paths.ProgramDir -Force | Out-Null
        }
        Copy-Item -Path $assetPath -Destination $paths.BinPath -Force
        Write-Log "installed binary to $($paths.BinPath)"

        $resolvedToken = Resolve-AgentToken
        if (-not $HubUrl) {
            throw '-HubUrl is required on first install'
        }
        New-Item -ItemType Directory -Path $paths.RequestDir -Force -ErrorAction SilentlyContinue | Out-Null
        New-Item -ItemType Directory -Path $paths.ResultDir -Force -ErrorAction SilentlyContinue | Out-Null

        Write-AgentEnvFile -EnvFilePath $paths.EnvFile -ResolvedHubUrl $HubUrl -ResolvedToken $resolvedToken `
            -ResolvedHostId $HostId -ResolvedRemoteUpdate ([bool]$RemoteUpdate)

        Set-CloudPulseAcl -Path $paths.DataDir -ServiceSid 'NT SERVICE\cloud-pulse-agent' -ServiceRights 'R'
        Set-CloudPulseAcl -Path $paths.RequestDir -ServiceSid 'NT SERVICE\cloud-pulse-agent' -ServiceRights 'M'
        Set-CloudPulseAcl -Path $paths.ResultDir -ServiceSid 'NT SERVICE\cloud-pulse-agent' -ServiceRights 'R'

        $binArgs = "`"$($paths.BinPath)`" --env-file `"$($paths.EnvFile)`""
        if (Test-ServiceInstalled -Name $CpServiceName) {
            Write-Log "service $CpServiceName already registered; restarting"
            Stop-CloudPulseService -Name $CpServiceName
        }
        else {
            Install-CloudPulseService -Name $CpServiceName -DisplayName 'cloud-pulse-agent' `
                -BinaryPathWithArgs $binArgs -StartAccount 'NT SERVICE\cloud-pulse-agent'
        }
        Start-CloudPulseService -Name $CpServiceName

        if ($RemoteUpdate) {
            $updaterArgs = "`"$($paths.BinPath)`" service run-updater --env-file `"$($paths.EnvFile)`""
            if (-not (Test-ServiceInstalled -Name $CpUpdaterServiceName)) {
                Install-CloudPulseService -Name $CpUpdaterServiceName -DisplayName 'cloud-pulse-agent-updater' `
                    -BinaryPathWithArgs $updaterArgs -StartAccount 'LocalSystem'
            }
            Start-CloudPulseService -Name $CpUpdaterServiceName
        }

        Write-Log 'cloud-pulse-agent installed successfully.'
        Write-Host "  Hub URL:      $HubUrl"
        Write-Host "  Config file:  $($paths.EnvFile)"
        Write-Host "  Service:      $CpServiceName"
        Write-Host ''
        Write-Host 'Future updates: cloud-pulse-agent update (run as Administrator)'
    }
    finally {
        Remove-Item -Path $tempDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Invoke-UninstallFlow {
    $paths = Get-InstallPaths
    Uninstall-CloudPulseService -Name $CpServiceName
    Uninstall-CloudPulseService -Name $CpUpdaterServiceName
    Remove-Item -Path $paths.BinPath -Force -ErrorAction SilentlyContinue
    Write-Log 'removed service(s) and binary'

    if ($Purge) {
        Remove-Item -Path $paths.EnvFile -Force -ErrorAction SilentlyContinue
        Remove-Item -Path $paths.RequestDir -Recurse -Force -ErrorAction SilentlyContinue
        Remove-Item -Path $paths.ResultDir -Recurse -Force -ErrorAction SilentlyContinue
        Write-Log 'purged config and remote-update state (-Purge)'
    }
    else {
        Write-Log "kept $($paths.EnvFile) (pass -Purge to remove it)"
    }
}

# ---------------------------------------------------------------------------
# Interactive menu
# ---------------------------------------------------------------------------

function Show-Menu {
    $paths = Get-InstallPaths
    $status = if (Test-Path $paths.BinPath) { 'installed' } else { 'not installed' }
    Write-Host 'cloud-pulse agent installer'
    Write-Host "  Status: $status"
    Write-Host '  1) Install'
    Write-Host '  2) Reinstall'
    Write-Host '  3) Uninstall'
    Write-Host '  0) Exit'
}

function Invoke-MenuLoop {
    Show-Menu
    $choice = Read-Host -Prompt 'Select [1-3, 0]'
    switch ($choice) {
        '1' {
            if (-not $HubUrl) { $HubUrl = Read-Host -Prompt 'Hub URL (e.g. http://100.x.y.z:8090)' }
            Invoke-InstallFlow
        }
        '2' { Invoke-InstallFlow }
        '3' {
            $confirm = Read-Host -Prompt 'Uninstall cloud-pulse-agent? [y/N]'
            if ($confirm -match '^[Yy]') {
                $purgeConfirm = Read-Host -Prompt 'Also delete configuration and tokens? [y/N]'
                if ($purgeConfirm -match '^[Yy]') { $Purge = $true }
                Invoke-UninstallFlow
            }
        }
        default { return }
    }
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

function Main {
    $anyActionGiven = $Install -or $Reinstall -or $Uninstall

    if (-not $anyActionGiven -and [Environment]::UserInteractive -and -not $Yes) {
        Invoke-MenuLoop
        return
    }

    if ($Uninstall) {
        Invoke-UninstallFlow
        return
    }

    $paths = Get-InstallPaths
    $installed = Test-Path $paths.BinPath
    if ($Install -and $installed) {
        throw 'already installed; use -Reinstall to upgrade'
    }
    if ($Reinstall -and -not $installed) {
        throw 'not installed; use -Install for a fresh install'
    }

    Invoke-InstallFlow
}

Main
