# End-to-end test of the Windows installer on a fresh Windows Server (a GitHub Actions runner):
# install from a release bundle, then deploy hot, broken and cold changes, a staff edit shipped
# over the file share, a database refresh, backups, and a re-run that must not touch the servers.
#
#   tests/e2e-windows.ps1 -Dist dist -Base main            (needs CFX_KEY, DEPLOY_KEY, E2E_REPO, E2E_BRANCH)
#   tests/e2e-windows.ps1 -CollectLogs out
#
# E2E_REPO is a test resources repo; DEPLOY_KEY may write to it. The test pushes to E2E_BRANCH only.
param(
    [string]$Dist = "",
    [string]$Base = "main",
    [string]$CollectLogs = ""
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$fx = "C:\Program Files\fxship\fxship.exe"
$data = "C:\ProgramData\fxship"

if ($CollectLogs) {
    New-Item -ItemType Directory -Force $CollectLogs | Out-Null
    Copy-Item "$data\logs\*" $CollectLogs -Recurse -ErrorAction SilentlyContinue
    if (Test-Path $fx) {
        & $fx status *> "$CollectLogs\status.json"
        & $fx doctor *> "$CollectLogs\doctor.txt"
    }
    Get-Service fxship* -ErrorAction SilentlyContinue | Format-Table -AutoSize | Out-File "$CollectLogs\services.txt"
    exit 0
}

$script:failures = 0
function Step($s) { Write-Host "`n=== $s" -ForegroundColor Cyan }
function Pass($s) { Write-Host "  ok  $s" -ForegroundColor Green }
function Fail($s) { Write-Host "  FAIL $s" -ForegroundColor Red; $script:failures++ }
function Check($cond, $s) { if ($cond) { Pass $s } else { Fail $s } }
function WaitFor([scriptblock]$cond, [int]$seconds, [string]$what) {
    $end = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $end) {
        try { if (& $cond) { Pass $what; return $true } } catch { }
        Start-Sleep 3
    }
    Fail "$what (gave up after ${seconds}s)"
    return $false
}
function Status { & $fx status | ConvertFrom-Json }
function Release($id) { (Status).releases | Where-Object { $_.id -eq $id } }
function Console($name) { Get-Content "$data\logs\fxship-$name\fxship-$name-console.log" -Raw -ErrorAction SilentlyContinue }
function Up($port) { try { (Invoke-WebRequest "http://127.0.0.1:$port/info.json" -TimeoutSec 3 -UseBasicParsing).StatusCode -eq 200 } catch { $false } }

foreach ($v in "CFX_KEY", "DEPLOY_KEY", "E2E_REPO", "E2E_BRANCH") {
    if (-not (Get-Item "env:$v" -ErrorAction SilentlyContinue).Value) { throw "$v isn't set" }
}
$repoUrl = "git@github.com:$($env:E2E_REPO).git"
$branch = $env:E2E_BRANCH

Step "Test repo branch $branch from $Base"
# The deploy key goes where the installer keeps it, readable by this admin only, as OpenSSH wants.
New-Item -ItemType Directory -Force "C:\fxship\.ssh" | Out-Null
$key = "C:\fxship\.ssh\deploy_key"
[IO.File]::WriteAllText($key, ($env:DEPLOY_KEY.Trim() -replace "`r", "") + "`n")
icacls $key /inheritance:r /grant:r "*S-1-5-18:F" /grant:r "*S-1-5-32-544:F" | Out-Null
$ssh = "C:/Windows/System32/OpenSSH/ssh.exe -i C:/fxship/.ssh/deploy_key -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=C:/e2e-known_hosts"
$env:GIT_SSH_COMMAND = $ssh
$work = "C:\e2e\repo"
git clone -q --branch $Base $repoUrl $work
if ($LASTEXITCODE) { throw "can't clone $repoUrl with the deploy key" }
$prefix = if (Test-Path "$work\server.cfg") { "resources\" } else { "" }
$probe = "$work\$prefix[local]\fxship_probe"
Push-Location $work
git config user.name "fxship e2e"; git config user.email "e2e@fxship.invalid"
# The probe also loads staff*.lua, so a file staff add over the share runs on dev.
Add-Content -LiteralPath "$probe\fxmanifest.lua" "server_script 'staff*.lua'"
git add -A | Out-Null; git commit -qm "e2e: probe loads staff files" | Out-Null
git push -q --force origin "HEAD:refs/heads/$branch" 2>&1 | Out-Null
Pop-Location
Pass "branch $branch at $(git -C $work rev-parse --short HEAD)"

function Push-Change([string]$msg, [scriptblock]$edit) {
    Push-Location $work
    & $edit | Out-Null
    git add -A | Out-Null
    git commit -qm $msg | Out-Null
    git push -q origin "HEAD:refs/heads/$branch" 2>&1 | Out-Null
    $sha = (git rev-parse --short=7 HEAD).Trim()
    Pop-Location
    & $fx poll | Out-Null
    return $sha
}

Step "Install from the bundle"
$bundle = Get-ChildItem "$Dist\fxship_*_windows_amd64.zip" | Select-Object -First 1
Expand-Archive $bundle.FullName "C:\e2e\bundle" -Force
$installer = (Get-ChildItem "C:\e2e\bundle\*\install.ps1").FullName
# Resources layout: server.cfg comes from the owner's old server, as it would on a real move.
$cfgSource = "starter"
if (-not $prefix) {
    $cfgSource = "C:\e2e\oldserver"
    New-Item -ItemType Directory -Force $cfgSource | Out-Null
    @"
sv_maxclients 32
set steam_webApiKey "none"
sets locale "en-GB"
endpoint_add_tcp "0.0.0.0:30120"
endpoint_add_udp "0.0.0.0:30120"
sv_licenseKey "cfxk_from_the_old_box"
ensure mapmanager
ensure spawnmanager
ensure basic-gamemode
ensure baseevents
ensure oxmysql
ensure fxship_probe
ensure testcar
"@ | Set-Content -Encoding ascii "$cfgSource\server.cfg"
}
@"
BRAND_NAME="E2E City"
TOPOLOGY="one-ip"
DEV_GAME_PORT="30130"
RESTART_TIMES="06:00,18:00"
REPO_URL="$repoUrl"
REPO_BRANCH="$branch"
CFG_SOURCE="$cfgSource"
PROD_LICENSE_KEY="$($env:CFX_KEY)"
TX_USER="admin"
FX_BUILD="recommended"
DB_INSTALL="true"
DISCORD_TOKEN=""
BACKUP_ENABLED="true"
BACKUP_REPO="C:\fxship-backups"
BACKUP_TIME="04:30"
TAILSCALE="false"
ADMIN_ALLOW_IPS="10.1.2.3"
"@ | Set-Content -Encoding ascii "C:\e2e\answers.env"
# Windows PowerShell 5.1, which is what an owner's elevated PowerShell runs.
$t = Measure-Command { powershell.exe -NoProfile -ExecutionPolicy Bypass -File $installer -Config "C:\e2e\answers.env" | Out-Host }
Check ($LASTEXITCODE -eq 0) "installer exits 0 ($([int]$t.TotalMinutes)m$($t.Seconds)s), fxship doctor passed"
Remove-Item "C:\e2e\answers.env"

Step "What got installed"
foreach ($s in "fxship", "fxship-prod", "fxship-dev", "fxship-mariadb") {
    Check ((Get-Service $s -ErrorAction SilentlyContinue).Status -eq 'Running') "service $s running"
}
$acct = (Get-CimInstance Win32_Service -Filter "Name='fxship-prod'").StartName
Check ($acct -eq 'NT SERVICE\fxship-prod') "fxship-prod runs as its own account ($acct)"
Check (Up 30120) "prod answers on 30120"
Check (Up 30130) "dev answers on 30130"
WaitFor { (Console prod) -match 'fxship_probe\] database ok' } 120 "prod's probe reached its database"
WaitFor { (Console dev) -match 'fxship_probe\] database ok' } 120 "dev's probe reached its database"
$st = Status
Check ($st.prodSha -and $st.prodSha -eq $st.devSha) "prod and dev on the same commit ($($st.prodSha.Substring(0,7)))"

Step "Who can read what"
$prodSecrets = (icacls "C:\fxship\prod\server-data\secrets.cfg") -join "`n"
Check ($prodSecrets -match 'fxship-prod' -and $prodSecrets -notmatch 'fxship-dev' -and $prodSecrets -notmatch 'Users') "prod's secrets.cfg: prod's account only"
$envAcl = (icacls "$data\fxship.env") -join "`n"
Check ($envAcl -notmatch 'fxship-prod|fxship-dev|Users|Everyone') "fxship.env: SYSTEM and Administrators only"
$keyAcl = (icacls $key) -join "`n"
Check ($keyAcl -notmatch 'fxship-prod|fxship-dev|Users|Everyone') "deploy key: SYSTEM and Administrators only"
$prodDir = (icacls "C:\fxship\prod") -join "`n"
Check ($prodDir -notmatch 'fxship-dev') "dev's account has no access to prod's folder"

Step "Hot change"
$v = Get-Random -Minimum 100 -Maximum 999
$sha = Push-Change "Probe: version $v" { (Get-Content -LiteralPath "$probe\server.lua") -replace 'local version = \d+', "local version = $v" | Set-Content -LiteralPath "$probe\server.lua" }
WaitFor { (Release $sha).devOk -eq $true } 240 "dev tested $sha"
WaitFor { (Console dev) -match "version $v running on dev" } 30 "dev runs version $v"
Check (-not ((Release $sha).resources | Where-Object { $_.cold })) "classed as a hot change"
& $fx approve | Out-Null
WaitFor { (Console prod) -match "version $v running on prod" } 120 "approved: prod runs version $v without a restart"
Check (Up 30120) "prod stayed up"

Step "Broken Lua is caught (luac.exe)"
$sha = Push-Change "Broken file" { Set-Content -LiteralPath "$probe\oops.lua" "local x = = 1" }
WaitFor { (Release $sha).lint.Count -gt 0 } 240 "syntax check flagged oops.lua"
Write-Host "      $(((Release $sha).lint | Select-Object -First 1).msg)"
& $fx reject | Out-Null
$sha = Push-Change "Remove broken file" { Remove-Item -LiteralPath "$probe\oops.lua" }
WaitFor { (Release $sha).devOk -eq $true } 240 "fix tested on dev"
& $fx approve | Out-Null
WaitFor { (Status).prodSha.StartsWith($sha) } 60 "fix live on prod"

Step "Cold change (streamed asset) waits, then apply-now"
$sha = Push-Change "Car: new texture" { Set-Content -LiteralPath "$work\$prefix[cars]\testcar\stream\note-$v.txt" "texture $v" }
WaitFor { (Release $sha).devOk -eq $true } 300 "dev restarted for it and passed"
Check (((Release $sha).resources | Where-Object { $_.cold }).Count -gt 0) "classed as needing a restart"
& $fx approve | Out-Null
WaitFor { (Release $sha).status -eq 'queued' } 30 "queued for the next scheduled restart"
$prodPid = (Get-CimInstance Win32_Service -Filter "Name='fxship-prod'").ProcessId
& $fx apply-now | Out-Null
WaitFor { (Release $sha).status -eq 'applied' } 420 "apply-now landed it"
WaitFor { Up 30120 } 300 "prod back up after the restart"
Check ((Get-CimInstance Win32_Service -Filter "Name='fxship-prod'").ProcessId -eq $prodPid) "txAdmin restarted the game, not the service"

Step "Staff edit over the share, shipped as the staff member"
$out = (& $fx staff add sam) -join "`n"
$pw = [regex]::Match($out, 'Password:\s+(\S+)').Groups[1].Value
Check ($pw.Length -ge 16) "fxship staff add sam"
$cred = New-Object PSCredential("$env:COMPUTERNAME\sam", (ConvertTo-SecureString $pw -AsPlainText -Force))
New-PSDrive -Name S -PSProvider FileSystem -Root "\\localhost\fxship-dev" -Credential $cred | Out-Null
$canRead = $true; try { Get-Content -LiteralPath "S:\secrets.cfg" -ErrorAction Stop | Out-Null } catch { $canRead = $false }
Check (-not $canRead) "staff can't read dev's secrets.cfg"
$canWrite = $true; try { Add-Content -LiteralPath "S:\instance.cfg" "endpoint_add_udp 0.0.0.0:30120" -ErrorAction Stop } catch { $canWrite = $false }
Check (-not $canWrite) "staff can't change instance.cfg (dev's ports)"
Set-Content -LiteralPath "S:\resources\[local]\fxship_probe\staff.lua" "print('[fxship_probe] staff edit $v')"
Remove-PSDrive S
WaitFor { (Status).devEdit.files.Count -gt 0 } 120 "edit picked up on dev"
WaitFor { (Console dev) -match "staff edit $v" } 60 "dev loaded the staff edit"
$staffFile = (Status).devEdit.files | Where-Object { $_.path -like '*staff.lua' }
Check ($staffFile.owner -eq 'sam') "the card credits staff.lua to sam (owner: $($staffFile.owner))"
& $fx ship "Staff edit from sam" | Out-Null
Check ($LASTEXITCODE -eq 0) "fxship ship"
# The commit is authored by whoever pressed Ship: here this admin, from the command line.
Push-Location $work; git pull -q origin $branch 2>&1 | Out-Null; $author = git log -1 --format='%an'; $files = git show --name-only --format= HEAD; Pop-Location
Check ($author -eq $env:USERNAME -and ($files -join ' ') -match 'staff.lua') "shipped to GitHub as $author with staff.lua"
& $fx staff remove sam | Out-Null

Step "Database refresh and backups"
& $fx db-refresh | Out-Null
Check ($LASTEXITCODE -eq 0) "dev database refreshed from prod"
WaitFor { Up 30130 } 300 "dev back up after the refresh"
& $fx backup run | Out-Null
Check ($LASTEXITCODE -eq 0) "backup run"
$snaps = (& $fx backup list) -join "`n"
Check ($snaps -match 'fxship') "backup list shows snapshots"
$restore = ("e2e_city_dev" | & $fx backup restore-db dev) -join "`n"
Check ($restore -match 'restored e2e_city_dev') "restore-db dev"

Step "Re-run with -Upgrade leaves the servers alone"
$before = (Get-CimInstance Win32_Service -Filter "Name='fxship-prod'").ProcessId
powershell.exe -NoProfile -ExecutionPolicy Bypass -File $installer -Upgrade | Out-Host
Check ($LASTEXITCODE -eq 0) "installer -Upgrade exits 0"
Check ((Get-CimInstance Win32_Service -Filter "Name='fxship-prod'").ProcessId -eq $before) "prod wasn't restarted"
Check (Up 30120) "prod still up"

Write-Host ""
if ($script:failures) { Write-Host "$($script:failures) check(s) failed" -ForegroundColor Red; exit 1 }
Write-Host "all checks passed" -ForegroundColor Green
