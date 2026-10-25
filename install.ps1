# Install or update only codex_switch.exe. This script never accesses Codex credentials.
$ErrorActionPreference = 'Stop'

$repository = if ($script:DefaultRepository) { $script:DefaultRepository } else { $env:CODEX_SWITCH_REPOSITORY }
$version = if ($script:DefaultVersion) { $script:DefaultVersion } else { $env:CODEX_SWITCH_VERSION }
$binDir = if ($env:CODEX_SWITCH_BIN_DIR) {
    $env:CODEX_SWITCH_BIN_DIR
} elseif ($env:LOCALAPPDATA) {
    Join-Path $env:LOCALAPPDATA 'Programs\CodexSwitch\bin'
} else {
    ''
}
$useGitHubCli = $false
$showHelp = $false

for ($index = 0; $index -lt $args.Count; $index++) {
    switch -CaseSensitive ($args[$index]) {
        { $_ -in @('--help', '-Help', '-h') } { $showHelp = $true; continue }
        '--repo' {
            if (++$index -ge $args.Count) { throw 'Missing value for --repo.' }
            $repository = $args[$index]
            continue
        }
        '--version' {
            if (++$index -ge $args.Count) { throw 'Missing value for --version.' }
            $version = $args[$index]
            continue
        }
        '--bin-dir' {
            if (++$index -ge $args.Count) { throw 'Missing value for --bin-dir.' }
            $binDir = $args[$index]
            continue
        }
        '--github-cli' { $useGitHubCli = $true; continue }
        default { throw "Unknown argument: $($args[$index])." }
    }
}

if ($showHelp) {
    Write-Output 'Usage: .\install.ps1 [--repo OWNER/REPO] [--version X.Y.Z] [--bin-dir PATH] [--github-cli] [--help]'
    exit 0
}

$workDir = $null
$stagePath = $null
$backupPath = $null
$lockStream = $null
$keepBackup = $false
$script:installStep = 'initialization'

function Stop-Install([string]$Message) {
    throw "codex_switch installer: ${script:installStep}: $Message"
}

function Set-PrivateDirectoryAcl([string]$Path) {
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $security = New-Object System.Security.AccessControl.DirectorySecurity
    $security.SetAccessRuleProtection($true, $false)
    $rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
        $identity.User,
        'FullControl',
        'ContainerInherit,ObjectInherit',
        'None',
        'Allow'
    )
    $security.AddAccessRule($rule)
    Set-Acl -LiteralPath $Path -AclObject $security
}

function Test-BelowPath([string]$Parent, [string]$Child) {
    $parentPath = [System.IO.Path]::GetFullPath($Parent).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
    $childPath = [System.IO.Path]::GetFullPath($Child)
    return $childPath.StartsWith($parentPath, [System.StringComparison]::OrdinalIgnoreCase)
}

function Invoke-VerifiedExecutable([string]$Path, [string]$ExpectedVersion) {
    $script:installStep = 'checking downloaded executable version'
    $versionOutput = @(& $Path --version 2>$null)
    $versionExitCode = $LASTEXITCODE
    if ($versionExitCode -ne 0) { Stop-Install 'The downloaded executable failed its version check.' }
    $reportedVersion = ($versionOutput -join "`n").Trim()
    if ($reportedVersion -notmatch '^([0-9]+\.[0-9]+\.[0-9]+) \(') { Stop-Install 'The executable returned an invalid version.' }
    if ($ExpectedVersion -and $Matches[1] -cne $ExpectedVersion) { Stop-Install 'The executable does not match the requested version.' }
    $script:installStep = 'checking downloaded executable help'
    $previousPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $null = @(& $Path -h 2>&1)
        $helpExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousPreference
    }
    if ($helpExitCode -ne 0) { Stop-Install "The downloaded executable failed its help check (exit code $helpExitCode)." }
    return $reportedVersion
}

function Add-UserPathEntry([string]$Path) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $entries = @($userPath -split ';' | Where-Object { $_ })
    $normalizedPath = [Environment]::ExpandEnvironmentVariables($Path).TrimEnd('\')
    foreach ($entry in $entries) {
        $normalizedEntry = [Environment]::ExpandEnvironmentVariables($entry.Trim().Trim('"')).TrimEnd('\')
        if ($normalizedEntry -ieq $normalizedPath) { return $false }
    }

    if (-not ('CodexSwitchEnvironment' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class CodexSwitchEnvironment {
    [DllImport("user32.dll", CharSet = CharSet.Auto, SetLastError = true)]
    public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint msg, UIntPtr wParam, string lParam, uint flags, uint timeout, out UIntPtr result);
}
'@
    }
    $updatedPath = if ($userPath) { "$userPath;$Path" } else { $Path }
    [Environment]::SetEnvironmentVariable('Path', $updatedPath, 'User')
    $result = [UIntPtr]::Zero
    [void][CodexSwitchEnvironment]::SendMessageTimeout([IntPtr]0xffff, 0x001A, [UIntPtr]::Zero, 'Environment', 0x0002, 5000, [ref]$result)
    return $true
}

try {
    $script:installStep = 'validating platform and arguments'
    if ($PSVersionTable.PSVersion -lt [Version]'5.1') { Stop-Install 'PowerShell 5.1 or later is required.' }
    $nativeArchitecture = $env:PROCESSOR_ARCHITEW6432
    if (-not $nativeArchitecture) { $nativeArchitecture = $env:PROCESSOR_ARCHITECTURE }
    if ($nativeArchitecture -cne 'AMD64') { Stop-Install 'This release supports Windows x64 only.' }
    $operatingSystem = [System.Environment]::OSVersion.Version
    if ($operatingSystem.Major -ne 10 -or $operatingSystem.Build -lt 22000) { Stop-Install 'This release supports Windows 11 only.' }
    if (-not $repository -or $repository -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') { Stop-Install 'Provide a GitHub repository with --repo OWNER/REPO.' }
    if ($version -and $version -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') { Stop-Install 'Version must use MAJOR.MINOR.PATCH.' }
    if (-not $binDir) { Stop-Install 'The installation directory cannot be empty.' }

    try { $binDir = [System.IO.Path]::GetFullPath($binDir) } catch { Stop-Install 'The installation path is invalid.' }
    if ($binDir.StartsWith('\\')) { Stop-Install 'Network paths are not supported.' }
    $driveRoot = [System.IO.Path]::GetPathRoot($binDir)
    $drive = New-Object System.IO.DriveInfo($driveRoot)
    if ($drive.DriveType -ne [System.IO.DriveType]::Fixed -or $drive.DriveFormat -cne 'NTFS') {
        Stop-Install 'Install on a local NTFS volume.'
    }
    $existingPath = $binDir
    while ($existingPath) {
        if (Test-Path -LiteralPath $existingPath) {
            $item = Get-Item -LiteralPath $existingPath -Force
            if ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) { Stop-Install 'The installation path contains a reparse point.' }
            if ($existingPath -ceq $binDir -and -not $item.PSIsContainer) { Stop-Install 'The installation path is a file.' }
        }
        $parent = [System.IO.Directory]::GetParent($existingPath)
        if (-not $parent) { break }
        $existingPath = $parent.FullName
    }
    $destination = Join-Path $binDir 'codex_switch.exe'
    $archiveName = 'codex_switch_windows_amd64.zip'

    $script:installStep = 'preparing private download directory'
    $workRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    $workDir = Join-Path $workRoot ('codex-switch-install-' + [Guid]::NewGuid().ToString('N'))
    [System.IO.Directory]::CreateDirectory($workDir) | Out-Null
    Set-PrivateDirectoryAcl $workDir

    $script:installStep = 'downloading release assets'
    $releaseTag = if ($version) { "v$version" } else { $null }
    if ($useGitHubCli) {
        if (-not (Get-Command gh -ErrorAction SilentlyContinue)) { Stop-Install 'Install and authenticate GitHub CLI first.' }
        if (-not $releaseTag) {
            $releaseTag = (& gh release view latest --repo $repository --json tagName --jq .tagName 2>$null | Out-String).Trim()
            if ($LASTEXITCODE -ne 0) { Stop-Install 'Could not resolve the latest GitHub release.' }
        }
        if ($releaseTag -cnotmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') { Stop-Install 'GitHub returned an invalid release tag.' }
        & gh release download $releaseTag --repo $repository --dir $workDir --pattern $archiveName --pattern checksums.txt
        if ($LASTEXITCODE -ne 0) { Stop-Install 'GitHub CLI could not download the release assets.' }
    } else {
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        if (-not $releaseTag) {
            $release = Invoke-RestMethod -Method Get -Uri "https://api.github.com/repos/$repository/releases/latest" -Headers @{ Accept = 'application/vnd.github+json'; 'User-Agent' = 'codex-switch-installer' } -TimeoutSec 45
            $releaseTag = [string]$release.tag_name
        }
        if ($releaseTag -cnotmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') { Stop-Install 'GitHub returned an invalid release tag.' }
        foreach ($asset in @($archiveName, 'checksums.txt')) {
            $uri = "https://github.com/$repository/releases/download/$releaseTag/$asset"
            $path = Join-Path $workDir $asset
            try { Invoke-WebRequest -Method Get -Uri $uri -OutFile $path -UseBasicParsing -TimeoutSec 120 } catch { Stop-Install 'Download failed. For a private repository, authenticate gh and use --github-cli.' }
            $info = Get-Item -LiteralPath $path
            if ($info.Length -le 0 -or $info.Length -gt 268435456) { Stop-Install "Downloaded asset has an invalid size: $asset." }
        }
    }
    if ($version -and $releaseTag -cne "v$version") { Stop-Install 'The resolved release tag does not match the requested version.' }

    foreach ($asset in @($archiveName, 'checksums.txt')) {
        $info = Get-Item -LiteralPath (Join-Path $workDir $asset)
        if ($info.Length -le 0 -or $info.Length -gt 268435456) { Stop-Install "Downloaded asset has an invalid size: $asset." }
    }

    $script:installStep = 'verifying release checksum'
    $manifestPath = Join-Path $workDir 'checksums.txt'
    $checksumLines = @(Get-Content -LiteralPath $manifestPath)
    $checksumMatches = @()
    foreach ($line in $checksumLines) {
        if ($line -match '^\s*([0-9a-fA-F]{64})\s+\*?([^\r\n]+?)\s*$' -and $Matches[2] -ceq $archiveName) {
            $checksumMatches += $Matches[1].ToLowerInvariant()
        }
    }
    if ($checksumMatches.Count -ne 1) { Stop-Install 'The checksum manifest must contain exactly one valid entry for the Windows archive.' }
    $actualChecksum = (Get-FileHash -LiteralPath (Join-Path $workDir $archiveName) -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualChecksum -cne $checksumMatches[0]) { Stop-Install 'The Windows archive checksum does not match.' }

    $script:installStep = 'validating Windows archive'
    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [System.IO.Compression.ZipFile]::OpenRead((Join-Path $workDir $archiveName))
    try {
        $seenEntries = New-Object 'System.Collections.Generic.HashSet[string]' ([System.StringComparer]::OrdinalIgnoreCase)
        $executables = @()
        foreach ($entry in $archive.Entries) {
            $name = $entry.FullName
            if (-not $name -or $name.Contains('\') -or $name.Contains(':') -or $name.StartsWith('/') -or $name -match '(^|/)\.\.?(/|$)' -or $name.Contains('//')) {
                Stop-Install 'The ZIP contains an unsafe entry name.'
            }
            if (-not $seenEntries.Add($name)) { Stop-Install 'The ZIP contains duplicate entry names.' }
            $unixType = ($entry.ExternalAttributes -shr 16) -band 0xF000
            $windowsAttributes = ($entry.ExternalAttributes -shr 16) -band 0xFFFF
            if ($unixType -eq 0xA000 -or ($windowsAttributes -band [int][System.IO.FileAttributes]::ReparsePoint)) {
                Stop-Install 'The ZIP contains a link or reparse entry.'
            }
            $isDirectory = $name.EndsWith('/')
            if ($unixType -ne 0 -and $unixType -ne 0x8000 -and $unixType -ne 0x4000) { Stop-Install 'The ZIP contains a non-regular entry.' }
            if ($isDirectory -and $unixType -notin @(0, 0x4000)) { Stop-Install 'The ZIP contains an invalid directory entry.' }
            if ($name -ceq 'codex_switch.exe' -and -not $isDirectory) {
                if ($entry.Length -le 0 -or $entry.Length -gt 268435456) { Stop-Install 'The executable entry has an invalid size.' }
                $executables += $entry
            }
        }
        if ($executables.Count -ne 1) { Stop-Install 'The ZIP must contain exactly one root codex_switch.exe.' }
        $candidate = Join-Path $workDir 'codex_switch.exe'
        $inputStream = $executables[0].Open()
        $outputStream = New-Object System.IO.FileStream($candidate, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
        try {
            $inputStream.CopyTo($outputStream)
            $outputStream.Flush($true)
        } finally {
            $outputStream.Dispose()
            $inputStream.Dispose()
        }
    } finally {
        $archive.Dispose()
    }
    $reportedVersion = Invoke-VerifiedExecutable $candidate $version

    $script:installStep = 'preparing install destination'
    [System.IO.Directory]::CreateDirectory($binDir) | Out-Null
    $lockPath = Join-Path $binDir '.codex_switch.install.lock'
    $lockDeadline = [DateTime]::UtcNow.AddSeconds(10)
    while (-not $lockStream) {
        try {
            $lockStream = New-Object System.IO.FileStream($lockPath, [System.IO.FileMode]::OpenOrCreate, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
        } catch [System.IO.IOException] {
            if ([DateTime]::UtcNow -ge $lockDeadline) { Stop-Install 'Another installer is busy; try again shortly.' }
            Start-Sleep -Milliseconds 100
        }
    }
    $script:installStep = 'staging executable'
    if (Test-Path -LiteralPath $destination) {
        $existing = Get-Item -LiteralPath $destination -Force
        if ($existing.PSIsContainer) { Stop-Install 'The destination is a directory.' }
        if ($existing.Attributes -band [System.IO.FileAttributes]::ReparsePoint) { Stop-Install 'The destination is a reparse point.' }
        if ($existing.Attributes -band [System.IO.FileAttributes]::ReadOnly) { Stop-Install 'The destination is read-only.' }
    }

    $stagePath = Join-Path $binDir ('.codex_switch.stage.' + [Guid]::NewGuid().ToString('N') + '.exe')
    [System.IO.File]::Copy($candidate, $stagePath, $false)
    $stageStream = New-Object System.IO.FileStream($stagePath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
    try { $stageStream.Flush($true) } finally { $stageStream.Dispose() }
    $candidateHash = (Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash
    if ((Get-FileHash -LiteralPath $stagePath -Algorithm SHA256).Hash -cne $candidateHash) { Stop-Install 'The staged executable changed during installation.' }

    $script:installStep = 'atomically replacing executable'
    if (Test-Path -LiteralPath $destination) {
        $backupPath = Join-Path $binDir ('.codex_switch.backup.' + [Guid]::NewGuid().ToString('N') + '.exe')
        try {
            [System.IO.File]::Replace($stagePath, $destination, $backupPath, $false)
            $stagePath = $null
        } catch {
            Stop-Install 'The installed executable is locked or could not be replaced. Close Codex Switch and retry.'
        }
        if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -cne $candidateHash) {
            $restoreFailure = $null
            try {
                [System.IO.File]::Replace($backupPath, $destination, $null, $false)
                $backupPath = $null
            } catch {
                $restoreFailure = $_
            }
            if ($restoreFailure) {
                $keepBackup = $true
                Stop-Install "Post-replacement verification failed and automatic recovery failed. The previous executable is at $backupPath."
            }
            Stop-Install 'The new executable failed post-replacement verification; the previous version was restored.'
        }
    } else {
        try { [System.IO.File]::Move($stagePath, $destination); $stagePath = $null } catch { Stop-Install 'The destination appeared or could not be installed; no existing executable was overwritten.' }
    }

    if ($backupPath) {
        try { [System.IO.File]::Delete($backupPath); $backupPath = $null } catch { Write-Warning 'The new version is installed, but a backup of the previous version could not be removed.' }
    }
    Write-Output "Installed codex_switch $reportedVersion"
    Write-Output "Location: $destination"
    try {
        if (Add-UserPathEntry $binDir) {
            Write-Output 'Added the install directory to your per-user PATH. Open a new terminal to run codex_switch.'
        } else {
            Write-Output 'The install directory is already in your per-user PATH. Open a new terminal if codex_switch is not recognized.'
        }
    } catch {
        Write-Warning "The executable is installed, but the per-user PATH could not be updated. Add this directory manually: $binDir"
    }
} catch {
    [Console]::Error.WriteLine("codex_switch installer failed during ${script:installStep}: $($_.Exception.Message)")
    exit 1
} finally {
    if ($lockStream) { try { $lockStream.Dispose() } catch { Write-Warning 'Could not release the installer lock cleanly.' } }
    if ($stagePath -and (Test-Path -LiteralPath $stagePath)) {
        try { [System.IO.File]::Delete($stagePath) } catch { Write-Warning "Could not remove staged executable $stagePath." }
    }
    if ($backupPath -and -not $keepBackup -and (Test-Path -LiteralPath $backupPath)) {
        try { [System.IO.File]::Delete($backupPath) } catch { Write-Warning "Could not remove backup executable $backupPath." }
    }
    if ($workDir -and (Test-Path -LiteralPath $workDir) -and (Test-BelowPath ([System.IO.Path]::GetTempPath()) $workDir)) {
        try { Remove-Item -LiteralPath $workDir -Recurse -Force } catch { Write-Warning 'Could not remove the private installer download directory.' }
    }
}
