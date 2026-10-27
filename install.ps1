# fxship installer for Windows Server 2022 and 2025. Safe to run again: every step checks what is
# already there, and a re-run is how settings change.
#
#   .\install.ps1                  interactive setup (or change settings)
#   .\install.ps1 -Config FILE     use the answers in FILE (fxship.env format), no questions
#   .\install.ps1 -Upgrade         re-apply everything with the saved answers (fxship upgrade)
#   irm https://github.com/RivoLabs/fxship/releases/latest/download/install.ps1 | iex
#
# Run it from an elevated PowerShell. Next to fxship.exe (an unpacked release) it installs that
# release; on its own it downloads the latest one, checks it against the release's checksums, and
# runs that.
param(
    [string]$Config = "",
    [switch]$Upgrade,
    [switch]$Yes,
    [string]$Version = ""
)
# A run piped into iex must not call exit: that would close the owner's PowerShell window.
$scriptDir = if ($PSScriptRoot) { $PSScriptRoot } else { "" }
$script:code = 0
& {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

    $admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    if (-not $admin) {
        Write-Host "Run this from an elevated PowerShell (right-click > Run as administrator)." -ForegroundColor Red
        $script:code = 1; return
    }
    if (-not [Environment]::Is64BitOperatingSystem) {
        Write-Host "FXServer needs 64-bit Windows." -ForegroundColor Red
        $script:code = 1; return
    }

    $here = $scriptDir
    $exe = if ($here) { Join-Path $here "fxship.exe" } else { "" }

    if (-not $exe -or -not (Test-Path $exe)) {
        # Piped from irm: fetch the release bundle, check it, and run its installer.
        $repo = if ($env:FXSHIP_REPO) { $env:FXSHIP_REPO } else { "RivoLabs/fxship" }
        if (-not $Version -and $env:FXSHIP_VERSION) { $Version = $env:FXSHIP_VERSION }
        $headers = @{ "User-Agent" = "fxship-installer"; "Accept" = "application/vnd.github+json" }
        if ($env:GITHUB_TOKEN) { $headers["Authorization"] = "Bearer $($env:GITHUB_TOKEN)" }
        $api = if ($Version) { "https://api.github.com/repos/$repo/releases/tags/$Version" } else { "https://api.github.com/repos/$repo/releases/latest" }
        $rel = Invoke-RestMethod -Uri $api -Headers $headers
        $tag = $rel.tag_name
        $name = "fxship_$($tag.TrimStart('v'))_windows_amd64.zip"
        $zipAsset = $rel.assets | Where-Object { $_.name -eq $name }
        $sumAsset = $rel.assets | Where-Object { $_.name -eq "checksums.txt" }
        if (-not $zipAsset -or -not $sumAsset) {
            Write-Host "Release $tag has no $name or checksums.txt." -ForegroundColor Red
            $script:code = 1; return
        }
        $dl = @{ "User-Agent" = "fxship-installer"; "Accept" = "application/octet-stream" }
        if ($env:GITHUB_TOKEN) { $dl["Authorization"] = "Bearer $($env:GITHUB_TOKEN)" }
        $dir = Join-Path $env:ProgramData "fxship\releases\$tag"
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
        $zip = Join-Path $dir $name
        Write-Host "Downloading fxship $tag..."
        Invoke-WebRequest -Uri $zipAsset.url -Headers $dl -OutFile $zip -UseBasicParsing
        $sums = (Invoke-WebRequest -Uri $sumAsset.url -Headers $dl -UseBasicParsing).Content
        if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
        $want = ($sums -split "`n" | Where-Object { $_ -match "\s$([regex]::Escape($name))\s*$" } | Select-Object -First 1) -replace '\s.*$', ''
        $got = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash.ToLower()
        if (-not $want -or $got -ne $want.ToLower()) {
            Remove-Item $zip -Force
            Write-Host "Checksum mismatch for $name, nothing was installed." -ForegroundColor Red
            $script:code = 1; return
        }
        Expand-Archive -Path $zip -DestinationPath $dir -Force
        $inner = Get-ChildItem -Path $dir -Directory | Where-Object { Test-Path (Join-Path $_.FullName "fxship.exe") } | Select-Object -First 1
        if (-not $inner) {
            Write-Host "The bundle has no fxship.exe." -ForegroundColor Red
            $script:code = 1; return
        }
        $exe = Join-Path $inner.FullName "fxship.exe"
    }

    $argv = @("install")
    if (-not $Config -and $env:FXSHIP_CONFIG) { $Config = $env:FXSHIP_CONFIG }
    if ($Config) { $argv += @("-config", (Resolve-Path $Config).Path) }
    if ($Upgrade) { $argv += "-upgrade" }
    if ($Yes) { $argv += "-yes" }
    # Not captured, so fxship.exe talks to the console directly and can ask its questions.
    & $exe @argv
    $script:code = $LASTEXITCODE
}
if ($scriptDir) { exit $script:code }
if ($script:code -ne 0) { Write-Host "fxship install stopped (exit code $($script:code))." -ForegroundColor Red }
