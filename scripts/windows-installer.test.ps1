param(
    [Parameter(Mandatory = $true)]
    [string]$PackageDir,
    [string]$InstallerPath = (Join-Path $PSScriptRoot '..\install.ps1'),
    [switch]$KeepTemp
)

$ErrorActionPreference = 'Stop'
$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$installer = [System.IO.Path]::GetFullPath($InstallerPath)
$expectedVersion = (Get-Content -LiteralPath (Join-Path $repoRoot 'internal/version/VERSION') -Raw).Trim()
$archiveName = 'codex_switch_windows_amd64.zip'
$packageArchive = Join-Path $PackageDir $archiveName
$packageChecksums = Join-Path $PackageDir 'checksums.txt'
foreach ($file in @($packageArchive, $packageChecksums)) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Missing Windows package asset: $file" }
}

$root = Join-Path ([System.IO.Path]::GetTempPath()) ('codex-switch-installer-test-' + [Guid]::NewGuid().ToString('N'))
$tools = Join-Path $root 'tools with spaces'
$assets = Join-Path $root 'assets'
$binDir = Join-Path $root 'installed tools'
$codexHome = Join-Path $root 'synthetic codex home'
$destination = Join-Path $binDir 'codex_switch.exe'
[System.IO.Directory]::CreateDirectory($tools) | Out-Null
[System.IO.Directory]::CreateDirectory($assets) | Out-Null
[System.IO.Directory]::CreateDirectory($binDir) | Out-Null
[System.IO.Directory]::CreateDirectory($codexHome) | Out-Null
[System.IO.File]::WriteAllText((Join-Path $codexHome 'auth.json'), '{"synthetic":"must remain unchanged"}' + "`n")
[System.IO.File]::WriteAllText($destination, 'previous executable')
[System.IO.File]::Copy($packageArchive, (Join-Path $assets $archiveName))
[System.IO.File]::Copy($packageChecksums, (Join-Path $assets 'checksums.txt'))

$node = (Get-Command node.exe -ErrorAction Stop).Source
$shim = @'
@echo off
"%NODE_EXE%" "%~dp0gh.js" %*
exit /b %ERRORLEVEL%
'@
$fakeGh = @'
const fs = require('node:fs');
const path = require('node:path');
const args = process.argv.slice(2);
if (args[0] !== 'release' || args[1] !== 'download') process.exit(20);
const destination = args[args.indexOf('--dir') + 1];
if (!destination) process.exit(21);
for (const name of ['codex_switch_windows_amd64.zip', 'checksums.txt']) {
  fs.copyFileSync(path.join(process.env.TEST_ASSETS, name), path.join(destination, name));
}
'@
[System.IO.File]::WriteAllText((Join-Path $tools 'gh.cmd'), $shim)
[System.IO.File]::WriteAllText((Join-Path $tools 'gh.js'), $fakeGh)

$originalPath = $env:PATH
$originalUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$originalRepo = $env:CODEX_SWITCH_REPOSITORY
$originalVersion = $env:CODEX_SWITCH_VERSION
$originalBinDir = $env:CODEX_SWITCH_BIN_DIR
$originalCodexHome = $env:CODEX_HOME
$originalTestAssets = $env:TEST_ASSETS
$originalNodeExe = $env:NODE_EXE
try {
    $env:PATH = "$tools$([System.IO.Path]::PathSeparator)$originalPath"
    $env:CODEX_SWITCH_REPOSITORY = 'example/switcher'
    $env:CODEX_SWITCH_VERSION = $expectedVersion
    $env:CODEX_SWITCH_BIN_DIR = $binDir
    $env:CODEX_HOME = $codexHome
    $env:TEST_ASSETS = $assets
    $env:NODE_EXE = $node

    $arguments = @('--github-cli', '--repo', 'example/switcher', '--version', $expectedVersion, '--bin-dir', $binDir)
    $previousErrorActionPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    for ($attempt = 0; $attempt -lt 2; $attempt++) {
        $output = @(& (Join-Path $PSHOME (Split-Path -Leaf (Get-Process -Id $PID).Path)) -NoProfile -ExecutionPolicy Bypass -File $installer @arguments 2>&1)
        $exitCode = $LASTEXITCODE
        if ($exitCode -ne 0) { throw "Installer attempt $($attempt + 1) failed ($exitCode): $($output -join ' ')" }
        $reported = @(& $destination --version 2>&1)
        if ($LASTEXITCODE -ne 0 -or ($reported -join "`n") -notmatch "^$([regex]::Escape($expectedVersion)) \(") {
            throw 'Installed Windows executable did not report the expected version.'
        }
    }
    $updatedUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (@($updatedUserPath -split ';') | Where-Object { $_.TrimEnd('\') -ieq $binDir.TrimEnd('\') })) {
        throw 'Installer did not add the install directory to the per-user PATH.'
    }
    if ((Get-Content -LiteralPath (Join-Path $codexHome 'auth.json') -Raw) -cne '{"synthetic":"must remain unchanged"}' + "`n") {
        throw 'Installer changed the synthetic Codex credentials fixture.'
    }
    $leftovers = @(Get-ChildItem -LiteralPath $binDir -Force | Where-Object { $_.Name -match '^\.codex_switch\.(stage|backup)\.' })
    if ($leftovers.Count -ne 0) { throw 'Installer left stage or backup files after a successful update.' }

    $checksumPath = Join-Path $assets 'checksums.txt'
    $validChecksums = [System.IO.File]::ReadAllBytes($checksumPath)
    [System.IO.File]::WriteAllText($checksumPath, ('0' * 64) + "  $archiveName`n")
    $installedHash = (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash
    $output = @(& (Join-Path $PSHOME (Split-Path -Leaf (Get-Process -Id $PID).Path)) -NoProfile -ExecutionPolicy Bypass -File $installer @arguments 2>&1)
    if ($LASTEXITCODE -eq 0 -or ($output -join ' ') -notmatch 'checksum does not match') {
        throw 'Installer did not reject a bad checksum before replacing the installed executable.'
    }
    if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -cne $installedHash) {
        throw 'A failed checksum validation changed the installed executable.'
    }
    [System.IO.File]::WriteAllBytes($checksumPath, $validChecksums)

    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archivePath = Join-Path $assets $archiveName
    $validArchive = [System.IO.File]::ReadAllBytes($archivePath)
    $sourceArchive = [System.IO.Compression.ZipFile]::OpenRead($packageArchive)
    try {
        $entry = $sourceArchive.GetEntry('codex_switch.exe')
        $sourceStream = $entry.Open()
        $payloadStream = New-Object System.IO.MemoryStream
        try { $sourceStream.CopyTo($payloadStream); $payload = $payloadStream.ToArray() } finally { $sourceStream.Dispose(); $payloadStream.Dispose() }
    } finally {
        $sourceArchive.Dispose()
    }
    $maliciousFile = New-Object System.IO.FileStream($archivePath, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    $maliciousZip = New-Object System.IO.Compression.ZipArchive($maliciousFile, [System.IO.Compression.ZipArchiveMode]::Create, $false)
    try {
        foreach ($name in @('../escape.exe', 'codex_switch.exe')) {
            $entry = $maliciousZip.CreateEntry($name)
            $entryStream = $entry.Open()
            try { $entryStream.Write($payload, 0, $payload.Length) } finally { $entryStream.Dispose() }
        }
    } finally {
        $maliciousZip.Dispose()
        $maliciousFile.Dispose()
    }
    $badArchiveHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    [System.IO.File]::WriteAllText($checksumPath, "$badArchiveHash  $archiveName`n")
    $output = @(& (Join-Path $PSHOME (Split-Path -Leaf (Get-Process -Id $PID).Path)) -NoProfile -ExecutionPolicy Bypass -File $installer @arguments 2>&1)
    if ($LASTEXITCODE -eq 0 -or ($output -join ' ') -notmatch 'unsafe entry name') {
        throw 'Installer did not reject a ZIP traversal entry.'
    }
    if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -cne $installedHash) {
        throw 'A ZIP traversal entry changed the installed executable.'
    }
    [System.IO.File]::WriteAllBytes($archivePath, $validArchive)
    [System.IO.File]::WriteAllBytes($checksumPath, $validChecksums)

    $junctionDir = Join-Path $root 'linked install directory'
    New-Item -ItemType Junction -Path $junctionDir -Target $binDir | Out-Null
    try {
        $junctionArguments = @('--github-cli', '--repo', 'example/switcher', '--version', $expectedVersion, '--bin-dir', $junctionDir)
        $output = @(& (Join-Path $PSHOME (Split-Path -Leaf (Get-Process -Id $PID).Path)) -NoProfile -ExecutionPolicy Bypass -File $installer @junctionArguments 2>&1)
        if ($LASTEXITCODE -eq 0 -or ($output -join ' ') -notmatch 'reparse point') {
            throw 'Installer did not reject a junction in the installation path.'
        }
        if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -cne $installedHash) {
            throw 'A reparse-point path changed the installed executable.'
        }
    } finally {
        [System.IO.Directory]::Delete($junctionDir)
    }

    $lockedFile = New-Object System.IO.FileStream($destination, [System.IO.FileMode]::Open, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
    try {
        $output = @(& (Join-Path $PSHOME (Split-Path -Leaf (Get-Process -Id $PID).Path)) -NoProfile -ExecutionPolicy Bypass -File $installer @arguments 2>&1)
        if ($LASTEXITCODE -eq 0 -or ($output -join ' ') -notmatch 'locked or could not be replaced') {
            throw 'Installer did not report a sharing violation while the executable was locked.'
        }
    } finally {
        $lockedFile.Dispose()
    }
    if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -cne $installedHash) {
        throw 'A sharing violation changed the installed executable.'
    }
    $ErrorActionPreference = $previousErrorActionPreference
    Write-Output "PASS: Windows installer installed and updated $expectedVersion, verified the package, preserved synthetic auth.json, rejected checksum, ZIP traversal and junction attacks, and preserved the executable during a sharing violation."
} finally {
    if ($previousErrorActionPreference) { $ErrorActionPreference = $previousErrorActionPreference }
    $env:PATH = $originalPath
    [Environment]::SetEnvironmentVariable('Path', $originalUserPath, 'User')
    $env:CODEX_SWITCH_REPOSITORY = $originalRepo
    $env:CODEX_SWITCH_VERSION = $originalVersion
    $env:CODEX_SWITCH_BIN_DIR = $originalBinDir
    $env:CODEX_HOME = $originalCodexHome
    $env:TEST_ASSETS = $originalTestAssets
    $env:NODE_EXE = $originalNodeExe
    if ($KeepTemp) {
        Write-Warning "Kept installer test fixtures at $root"
    } elseif (Test-Path -LiteralPath $root) {
        Remove-Item -LiteralPath $root -Recurse -Force
    }
}

# Expected failing child processes must not become the harness exit code.
exit 0
