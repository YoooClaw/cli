# @yoooclaw/cli Windows installer
#
# One-command install (PowerShell 5.1+):
#   irm https://artifact.yoooclaw.com/cli/install.ps1 | iex
#
# Install with options:
#   & ([scriptblock]::Create((irm https://artifact.yoooclaw.com/cli/install.ps1))) -Version 0.9.1 -Force
#
# The installer downloads the native Go executable. Node.js and npm are not
# required, and installation is per-user by default (no administrator rights).

# A script-level param block cannot be parsed by `Invoke-Expression` in Windows
# PowerShell 5.1. Capture the automatic argument list and forward it into an
# advanced child script block instead, which supports both `irm ... | iex` and
# `& ([scriptblock]::Create((irm ...))) -Version ...`.
$yoooclawInstallerArguments = @($args)
try {
& {
[CmdletBinding()]
param(
    [string] $Version = "",
    [switch] $Beta,
    [string] $InstallDir = "",
    [switch] $Force,
    [switch] $Activate,
    [string] $HermesProfile = "",
    [switch] $NoModifyPath,
    [switch] $KeepNpm,
    # Primarily useful for mirrors and installer integration tests. The layout
    # must be <BaseUrl>/v<version>/{asset,checksums.txt} with latest/beta markers.
    [string] $BaseUrl = ""
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = "Stop"

# These placeholders are rendered by tools/oss-upload during a release. The
# source version falls back to GitHub Releases, while the rendered version uses
# the OSS mirror and its latest/beta marker files.
$OssBaseUrl = "__YOOOCLAW_CLI_OSS_BASE_URL__"
$OssRendered = "__YOOOCLAW_CLI_TEMPLATE_RENDERED__"
$Repository = "YoooClaw/cli"
$Asset = "yoooclaw-win32-x64.exe"

function Write-Info([string] $Message) {
    Write-Host "==> $Message" -ForegroundColor Blue
}

function Write-WarningMessage([string] $Message) {
    Write-Warning $Message
}

function Assert-SupportedPlatform {
    if ($env:OS -ne "Windows_NT") {
        throw "This installer only supports Windows. Use scripts/install.sh on macOS or Linux."
    }

    # An x64 build is currently published. Windows on ARM can run it through
    # the OS x64 compatibility layer; 32-bit-only Windows is not supported.
    $architecture = $env:PROCESSOR_ARCHITEW6432
    if ([string]::IsNullOrWhiteSpace($architecture)) {
        $architecture = $env:PROCESSOR_ARCHITECTURE
    }
    if ($architecture -notin @("AMD64", "ARM64")) {
        throw "Unsupported Windows architecture: $architecture (x64 or Windows on ARM is required)."
    }
}

function Get-WebText([string] $Uri) {
    $headers = @{ "User-Agent" = "yoooclaw-installer" }
    $response = Invoke-WebRequest -UseBasicParsing -Headers $headers -Uri $Uri
    return [string] $response.Content
}

function Save-WebFile([string] $Uri, [string] $Destination) {
    $headers = @{ "User-Agent" = "yoooclaw-installer" }
    Invoke-WebRequest -UseBasicParsing -Headers $headers -Uri $Uri -OutFile $Destination
}

function Resolve-Version {
    if (-not [string]::IsNullOrWhiteSpace($Version)) {
        return $Version.Trim()
    }

    $channel = "latest"
    if ($Beta) {
        $channel = "beta"
    }

    $markerBaseUrl = ""
    if (-not [string]::IsNullOrWhiteSpace($BaseUrl)) {
        $markerBaseUrl = $BaseUrl.TrimEnd("/")
    }
    elseif ($OssRendered -eq "1") {
        $markerBaseUrl = $OssBaseUrl.TrimEnd("/")
    }

    if ($markerBaseUrl) {
        Write-Info "Resolving $channel version..."
        return (Get-WebText "$markerBaseUrl/$channel").Trim()
    }

    Write-Info "Resolving the latest GitHub release..."
    $headers = @{ "Accept" = "application/vnd.github+json"; "User-Agent" = "yoooclaw-installer" }
    $releases = @(Invoke-RestMethod -Headers $headers -Uri "https://api.github.com/repos/$Repository/releases?per_page=100")
    $tags = @($releases | ForEach-Object { [string] $_.tag_name } | Where-Object { $_ -match '^cli-v' })
    if ($Beta) {
        $tag = $tags | Where-Object { $_ -match '-(beta|alpha|rc)([.-]|$)' } | Select-Object -First 1
    }
    else {
        $tag = $tags | Where-Object { $_ -notmatch '-(beta|alpha|rc)([.-]|$)' } | Select-Object -First 1
    }
    if ([string]::IsNullOrWhiteSpace($tag)) {
        throw "Unable to resolve the $channel release. Specify -Version explicitly."
    }
    return $tag.Substring("cli-v".Length)
}

function Get-ArtifactBaseUrl([string] $ResolvedVersion) {
    if (-not [string]::IsNullOrWhiteSpace($BaseUrl)) {
        return $BaseUrl.TrimEnd("/") + "/v" + $ResolvedVersion
    }
    if ($OssRendered -eq "1") {
        return $OssBaseUrl.TrimEnd("/") + "/v" + $ResolvedVersion
    }
    return "https://github.com/$Repository/releases/download/cli-v$ResolvedVersion"
}

function Get-DefaultInstallDir {
    if (-not [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        return Join-Path $env:LOCALAPPDATA "YoooClaw\bin"
    }
    return Join-Path $HOME ".yoooclaw\bin"
}

function Test-PathEntry([string] $PathValue, [string] $Entry) {
    if ([string]::IsNullOrWhiteSpace($PathValue)) {
        return $false
    }
    $fullEntry = [IO.Path]::GetFullPath($Entry).TrimEnd("\")
    foreach ($item in ($PathValue -split ";")) {
        if ([string]::IsNullOrWhiteSpace($item)) {
            continue
        }
        try {
            if ([IO.Path]::GetFullPath($item.Trim()).TrimEnd("\") -ieq $fullEntry) {
                return $true
            }
        }
        catch {
            # Preserve malformed or environment-variable-based PATH entries; they
            # simply do not match our absolute installation directory.
        }
    }
    return $false
}

function Add-InstallDirToPath([string] $Directory) {
    if (-not (Test-PathEntry $env:Path $Directory)) {
        $env:Path = "$Directory;$($env:Path)"
    }

    if ($NoModifyPath) {
        Write-WarningMessage "PATH was not persisted because -NoModifyPath was supplied."
        return
    }

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (Test-PathEntry $userPath $Directory) {
        Write-Info "User PATH is already configured."
        return
    }
    if ([string]::IsNullOrWhiteSpace($userPath)) {
        $newUserPath = $Directory
    }
    else {
        $newUserPath = $Directory + ";" + $userPath.TrimStart(";")
    }
    try {
        [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
    }
    catch {
        Write-WarningMessage "Could not persist the user PATH. The command works in this terminal; add '$Directory' manually for future terminals."
        return
    }
    try {
        # Notify Explorer and already-running terminal hosts that the persisted
        # environment changed. The current PowerShell process was updated above.
        if ($null -eq ("YoooClawInstaller.NativeMethods" -as [type])) {
            Add-Type -Namespace YoooClawInstaller -Name NativeMethods -MemberDefinition @'
[System.Runtime.InteropServices.DllImport("user32.dll", SetLastError=true, CharSet=System.Runtime.InteropServices.CharSet.Auto)]
public static extern System.IntPtr SendMessageTimeout(
    System.IntPtr hWnd,
    uint Msg,
    System.UIntPtr wParam,
    string lParam,
    uint flags,
    uint timeout,
    out System.UIntPtr result);
'@
        }
        $broadcastResult = [UIntPtr]::Zero
        [void] [YoooClawInstaller.NativeMethods]::SendMessageTimeout(
            [IntPtr] 0xffff,
            [uint32] 0x001a,
            [UIntPtr]::Zero,
            "Environment",
            [uint32] 0x0002,
            [uint32] 1000,
            [ref] $broadcastResult
        )
    }
    catch {
        Write-WarningMessage "PATH was saved, but other running apps could not be notified. Reopen the terminal if needed."
    }
    Write-Info "Added $Directory to the user PATH."
}

function Find-NpmCommand {
    $command = Get-Command "npm.cmd" -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $command) {
        return [string] $command.Path
    }
    return ""
}

function Test-NpmCliInstalled([string] $Npm) {
    if ([string]::IsNullOrWhiteSpace($Npm)) {
        return $false
    }
    try {
        $rootOutput = @(& $Npm root --global 2>$null)
        $rootExitCode = $LASTEXITCODE
        if ($rootExitCode -ne 0 -or $rootOutput.Count -eq 0) {
            return $false
        }
        $globalRoot = ([string] ($rootOutput | Select-Object -First 1)).Trim()
        if ([string]::IsNullOrWhiteSpace($globalRoot)) {
            return $false
        }
        return Test-Path -LiteralPath (Join-Path $globalRoot "@yoooclaw\cli\package.json") -PathType Leaf
    }
    catch {
        return $false
    }
}

function Remove-NpmCli([string] $Npm) {
    Write-Info "Removing the previous global npm installation..."
    try {
        & $Npm uninstall --global "@yoooclaw/cli"
        if ($LASTEXITCODE -eq 0) {
            Write-Info "Removed previous npm package: @yoooclaw/cli"
            return $true
        }
        Write-WarningMessage "The native CLI is ready, but npm returned exit code $LASTEXITCODE while removing @yoooclaw/cli. Run 'npm uninstall -g @yoooclaw/cli' manually."
    }
    catch {
        Write-WarningMessage "The native CLI is ready, but the old npm package could not be removed. Run 'npm uninstall -g @yoooclaw/cli' manually. $($_.Exception.Message)"
    }
    return $false
}

function Find-ExistingCli([string] $Target) {
    if (Test-Path -LiteralPath $Target -PathType Leaf) {
        return $Target
    }
    foreach ($name in @("yoooclaw.exe", "yoooclaw")) {
        $commands = @(Get-Command $name -All -ErrorAction SilentlyContinue)
        foreach ($command in $commands) {
            if ($command.CommandType -notin @("Application", "ExternalScript")) {
                continue
            }
            $source = [string] $command.Path
            if ($source.EndsWith(".ps1", [StringComparison]::OrdinalIgnoreCase)) {
                $cmdShim = [IO.Path]::ChangeExtension($source, ".cmd")
                if (Test-Path -LiteralPath $cmdShim -PathType Leaf) {
                    return $cmdShim
                }
            }
            return $source
        }
    }
    return ""
}

function Invoke-CliQuiet([string] $Cli, [string[]] $Arguments) {
    & $Cli @Arguments *> $null
    return ($LASTEXITCODE -eq 0)
}

function Stop-ExistingDaemons([string] $Cli) {
    $stopped = @()
    if ([string]::IsNullOrWhiteSpace($Cli)) {
        return $stopped
    }

    if (-not [string]::IsNullOrWhiteSpace($env:YOOOCLAW_HOME)) {
        $runtimeRoot = $env:YOOOCLAW_HOME
    }
    else {
        $runtimeRoot = Join-Path $HOME ".yoooclaw"
    }
    $profilesRoot = Join-Path $runtimeRoot "profiles"
    $profiles = @()
    if (Test-Path -LiteralPath $profilesRoot -PathType Container) {
        $profiles = @(Get-ChildItem -LiteralPath $profilesRoot -Directory | ForEach-Object { $_.Name })
    }
    if ($profiles.Count -eq 0) {
        $profiles = @("default")
    }

    foreach ($profile in $profiles) {
        if (Invoke-CliQuiet $Cli @("--profile", $profile, "daemon", "status")) {
            if (-not (Invoke-CliQuiet $Cli @("--profile", $profile, "daemon", "stop"))) {
                throw "Unable to stop the existing CLI daemon for profile '$profile'."
            }
            $stopped += $profile
            Write-Info "Stopped existing CLI daemon: $profile"
        }
    }
    return $stopped
}

function Restore-Daemons([string] $Cli, [string[]] $Profiles) {
    foreach ($profile in $Profiles) {
        if (Invoke-CliQuiet $Cli @("--profile", $profile, "daemon", "autostart", "enable")) {
            Write-Info "Restored CLI daemon and login autostart: $profile"
        }
        else {
            Write-WarningMessage "Could not configure login autostart; restoring detached daemon for '$profile'."
            if (-not (Invoke-CliQuiet $Cli @("--profile", $profile, "daemon", "start"))) {
                throw "The CLI was updated, but daemon profile '$profile' could not be restored."
            }
        }
    }
}

Assert-SupportedPlatform

# PowerShell 5.1 may otherwise negotiate TLS 1.0 on older Windows installs.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$resolvedVersion = Resolve-Version
if ($resolvedVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z][0-9A-Za-z.+-]*)?$') {
    throw "Invalid version: $resolvedVersion"
}
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    $InstallDir = Get-DefaultInstallDir
}
$InstallDir = [IO.Path]::GetFullPath([Environment]::ExpandEnvironmentVariables($InstallDir))
$target = Join-Path $InstallDir "yoooclaw.exe"
$alias = Join-Path $InstallDir "yc.exe"

if (((Test-Path -LiteralPath $target) -or (Test-Path -LiteralPath $alias)) -and -not $Force) {
    throw "$target or $alias already exists. Re-run with -Force to update it."
}

$npmCommand = ""
$npmCliInstalled = $false
if (-not $KeepNpm) {
    $npmCommand = Find-NpmCommand
    $npmCliInstalled = Test-NpmCliInstalled $npmCommand
    if ($npmCliInstalled) {
        Write-Info "Detected previous global npm package: @yoooclaw/cli"
    }
}

$artifactBaseUrl = Get-ArtifactBaseUrl $resolvedVersion
Write-Info "Target $Asset  version $resolvedVersion"

$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ("yoooclaw-install-" + [guid]::NewGuid().ToString("N"))
$downloaded = Join-Path $tempRoot $Asset
$backupSuffix = ".installer-backup-" + [guid]::NewGuid().ToString("N")
$targetBackup = $target + $backupSuffix
$aliasBackup = $alias + $backupSuffix
$targetOriginallyExisted = Test-Path -LiteralPath $target
$aliasOriginallyExisted = Test-Path -LiteralPath $alias
$stoppedProfiles = @()
$handoffPending = $false
$installationCommitted = $false
$targetBackedUp = $false
$aliasBackedUp = $false

try {
    New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null

    Write-Info "Downloading $artifactBaseUrl/$Asset"
    Save-WebFile "$artifactBaseUrl/$Asset" $downloaded

    Write-Info "Downloading checksums.txt"
    try {
        $checksums = Get-WebText "$artifactBaseUrl/checksums.txt"
        $match = [regex]::Match($checksums, "(?mi)^([0-9a-f]{64})\s+\*?$([regex]::Escape($Asset))\s*$")
        if (-not $match.Success) {
            throw "checksums.txt does not contain $Asset"
        }
        $expectedHash = $match.Groups[1].Value.ToLowerInvariant()
        $actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $downloaded).Hash.ToLowerInvariant()
        if ($actualHash -ne $expectedHash) {
            throw "SHA-256 mismatch: expected $expectedHash, got $actualHash"
        }
        Write-Info "SHA-256 verified: $actualHash"
    }
    catch {
        # Match install.sh compatibility for old releases that did not publish a
        # checksum manifest, but never ignore a checksum mismatch once parsed.
        if ($_.Exception.Message -match '^SHA-256 mismatch:' -or $_.Exception.Message -match '^checksums\.txt does not contain') {
            throw
        }
        Write-WarningMessage "checksums.txt is unavailable; checksum verification was skipped. $($_.Exception.Message)"
    }

    # Download and verify before briefly draining existing standalone daemons.
    $oldCli = Find-ExistingCli $target
    $stoppedProfiles = @(Stop-ExistingDaemons $oldCli)
    $handoffPending = ($stoppedProfiles.Count -gt 0)

    if (Test-Path -LiteralPath $target) {
        Move-Item -LiteralPath $target -Destination $targetBackup -Force
        $targetBackedUp = $true
    }
    if (Test-Path -LiteralPath $alias) {
        Move-Item -LiteralPath $alias -Destination $aliasBackup -Force
        $aliasBackedUp = $true
    }

    Move-Item -LiteralPath $downloaded -Destination $target -Force
    Copy-Item -LiteralPath $target -Destination $alias -Force

    $installedVersionOutput = @(& $target --version 2>$null)
    $versionExitCode = $LASTEXITCODE
    $installedVersion = ([string] ($installedVersionOutput | Select-Object -First 1)).Trim()
    if ($versionExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($installedVersion)) {
        throw "The executable was written, but yoooclaw --version failed."
    }
    if ($installedVersion -ne $resolvedVersion) {
        throw "Installed version '$installedVersion' does not match requested version '$resolvedVersion'."
    }
    Add-InstallDirToPath $InstallDir
    $installationCommitted = $true

    if ($npmCliInstalled) {
        $npmRemoved = Remove-NpmCli $npmCommand
        if (-not $npmRemoved) {
            Write-WarningMessage "The new native commands take precedence in PATH; the leftover npm package will not block their use."
        }
    }
    Write-Info "Installed: $target"
    Write-Info "Installed: $alias"
    Write-Info "yoooclaw $installedVersion ready"

    $activateOwner = $Activate -or ($env:YOOOCLAW_ACTIVATE_OWNER -eq "cli")
    if ($activateOwner) {
        Write-Info "Switching Relay owner to the standalone CLI..."
        $ownerArgs = @("owner", "activate", "cli")
        if (-not [string]::IsNullOrWhiteSpace($HermesProfile)) {
            $ownerArgs += @("--hermes-profile", $HermesProfile)
        }
        & $target @ownerArgs
        if ($LASTEXITCODE -ne 0) {
            throw "The CLI was installed, but Relay owner activation failed."
        }
    }
    else {
        Restore-Daemons $target $stoppedProfiles
        Write-Info "Current Relay owner was preserved. Use -Activate to switch it to the standalone CLI."
    }

    if (Invoke-CliQuiet $target @("daemon", "autostart", "migrate", "--format", "json")) {
        Write-Info "Daemon login-autostart state checked."
    }
    else {
        Write-WarningMessage "Could not migrate daemon login autostart; run 'yoooclaw daemon autostart enable' later."
    }
    $handoffPending = $false
}
catch {
    if (-not $installationCommitted) {
        if (($targetBackedUp -or -not $targetOriginallyExisted) -and (Test-Path -LiteralPath $target)) {
            Remove-Item -LiteralPath $target -Force
        }
        if (($aliasBackedUp -or -not $aliasOriginallyExisted) -and (Test-Path -LiteralPath $alias)) {
            Remove-Item -LiteralPath $alias -Force
        }
        if ($targetBackedUp -and (Test-Path -LiteralPath $targetBackup)) {
            Move-Item -LiteralPath $targetBackup -Destination $target -Force
        }
        if ($aliasBackedUp -and (Test-Path -LiteralPath $aliasBackup)) {
            Move-Item -LiteralPath $aliasBackup -Destination $alias -Force
        }
    }

    if ($handoffPending -and $stoppedProfiles.Count -gt 0) {
        Write-WarningMessage "Installation did not finish cleanly; restoring previously running CLI daemons."
        $recoveryCli = Find-ExistingCli $target
        if (-not [string]::IsNullOrWhiteSpace($recoveryCli)) {
            foreach ($profile in $stoppedProfiles) {
                if (-not (Invoke-CliQuiet $recoveryCli @("--profile", $profile, "daemon", "start"))) {
                    Write-WarningMessage "Could not automatically restore daemon profile '$profile'."
                }
            }
        }
    }
    throw
}
finally {
    if (Test-Path -LiteralPath $tempRoot) {
        Remove-Item -LiteralPath $tempRoot -Recurse -Force
    }
    if ($installationCommitted) {
        if (Test-Path -LiteralPath $targetBackup) {
            Remove-Item -LiteralPath $targetBackup -Force
        }
        if (Test-Path -LiteralPath $aliasBackup) {
            Remove-Item -LiteralPath $aliasBackup -Force
        }
    }
}

Write-Host ""
Write-Host "Installation complete. Try: yoooclaw --help" -ForegroundColor Green
} @yoooclawInstallerArguments
}
finally {
    Remove-Variable -Name yoooclawInstallerArguments -ErrorAction SilentlyContinue
}
