# End-to-end test of install.ps1 on Windows with the binary built from the
# working tree (served as a release zip + checksums.txt through
# AUTOMODEL_BASE_URL). CI runners only: it installs into this user's profile
# (settings.json, config, HKCU Run key, user PATH), then uninstalls.
#   pwsh scripts/install-test.ps1
$ErrorActionPreference = 'Stop'
$ver = '0.0.0-test'
$root = Split-Path -Parent $PSScriptRoot
$listen = '127.0.0.1:8788'
$settings = Join-Path $env:USERPROFILE '.claude\settings.json'
$cfg = Join-Path $env:USERPROFILE '.config\automodel\config.toml'
$stateDir = Join-Path $env:USERPROFILE '.local\state\automodel'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'

function Fail($msg) { Write-Host "install test: $msg" -ForegroundColor Red; exit 1 }
function Step($msg) { Write-Host "--- $msg" -ForegroundColor Cyan }
function Up { try { Invoke-RestMethod -TimeoutSec 2 "http://$listen/automodel/health" } catch { $null } }
function WaitFor([scriptblock]$cond, $what) {
	for ($i = 0; $i -lt 100; $i++) { if (& $cond) { return }; Start-Sleep -Milliseconds 100 }
	Fail "timed out: $what"
}
# Git Bash, which Claude Code runs hook and statusline commands with.
$gitRoot = Split-Path -Parent (Split-Path -Parent (Get-Command git).Source) # <Git>\cmd\git.exe
$bash = @("$gitRoot\bin\bash.exe", "$(Split-Path -Parent $gitRoot)\bin\bash.exe", "$env:ProgramFiles\Git\bin\bash.exe") |
	Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $bash) { Fail 'no Git Bash' }

# Runs a settings.json command the way Claude Code does, with JSON on stdin:
# through Git Bash (the default), or PowerShell (without Git for Windows).
function RunCommand($shell, $command, $json) {
	$f = New-TemporaryFile
	Set-Content -LiteralPath $f -Value $json -NoNewline
	try {
		if ($shell -eq 'bash') { $out = Get-Content -Raw $f | & $bash -c $command }
		else { $out = Get-Content -Raw $f | & pwsh -NoProfile -NonInteractive -Command $command }
		if ($LASTEXITCODE -ne 0) { Fail "$shell exited $LASTEXITCODE running: $command" }
		return ($out -join "`n")
	} finally { Remove-Item $f }
}
function Settings { Get-Content -Raw $settings | ConvertFrom-Json }
function HookCmd($event) { (Settings).hooks.$event[0].hooks[0].command }

Step 'build the release zip'
$arch = if ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') { 'arm64' } else { 'amd64' }
$dist = Join-Path ([IO.Path]::GetTempPath()) ("automodel-dist-" + [Guid]::NewGuid())
New-Item -ItemType Directory $dist | Out-Null
$env:CGO_ENABLED = '0'
go build -C $root -ldflags "-X main.version=$ver" -o (Join-Path $dist 'automodel.exe') ./cmd/automodel
if ($LASTEXITCODE -ne 0) { Fail 'go build' }
$zip = "automodel_${ver}_windows_${arch}.zip"
Compress-Archive -Path (Join-Path $dist 'automodel.exe') -DestinationPath (Join-Path $dist $zip)
Remove-Item (Join-Path $dist 'automodel.exe')
$sum = (Get-FileHash -Algorithm SHA256 (Join-Path $dist $zip)).Hash.ToLower()
Set-Content -LiteralPath (Join-Path $dist 'checksums.txt') -Value "$sum  $zip"
$env:AUTOMODEL_VERSION = "v$ver"
$env:AUTOMODEL_BASE_URL = ([Uri]$dist).AbsoluteUri # file:///C:/...

# An existing statusline, to be chained and restored.
New-Item -ItemType Directory -Force (Split-Path $settings) | Out-Null
Set-Content -LiteralPath $settings -Value '{"statusLine": {"type": "command", "command": "echo mine"}}'

Step 'install with Windows PowerShell 5.1, the documented way (irm | iex)'
& powershell.exe -NoProfile -NonInteractive -Command "Get-Content -Raw '$root\install.ps1' | Invoke-Expression"
if ($LASTEXITCODE -ne 0) { Fail "install.ps1 (5.1) exited $LASTEXITCODE" }
$binDir = Join-Path $env:LOCALAPPDATA 'automodel\bin'
$bin = Join-Path $binDir 'automodel.exe'
if (-not (Test-Path $bin)) { Fail "$bin missing" }
if (([Environment]::GetEnvironmentVariable('Path', 'User') -split ';') -notcontains $binDir) { Fail 'bin dir not in the user PATH' }
if (-not ((Up).version -eq $ver)) { Fail "proxy not answering on $listen" }
# The proxy must not install a real release during the test.
Add-Content -LiteralPath $cfg -Value "`n[update]`nauto = false"

Step 'reinstall over the running proxy (pwsh 7, -File)'
& pwsh -NoProfile -NonInteractive -File (Join-Path $root 'install.ps1')
if ($LASTEXITCODE -ne 0) { Fail "install.ps1 (pwsh) exited $LASTEXITCODE" }
WaitFor { (Up).version -eq $ver } 'proxy after reinstall'
WaitFor { -not (Test-Path "$bin.old") } 'old binary removed by the new proxy'

Step 'doctor'
$doctor = & $bin doctor
$doctor | Write-Host
if ($LASTEXITCODE -ne 0) { Fail 'doctor reported a failure' }
if (-not ($doctor -match 'service\s+logon: background process')) { Fail 'service is not logon' }
if (-not (Get-Content -Raw $settings).Contains("""ANTHROPIC_BASE_URL"": ""http://$listen""")) { Fail 'settings.json not written' }
$run = (Get-ItemProperty $runKey).automodel
if (-not ($run -like '*automodel.exe*--config*start')) { Fail "logon entry: $run" }
if (-not ((& $bin start) -match 'already running')) { Fail 'start restarted a running proxy' }

Step 'hooks and statusline as Claude Code runs them'
foreach ($shell in 'bash', 'pwsh') {
	$cmd = HookCmd 'SessionStart'
	if ($cmd -match '\\') { Fail "backslashes in a hook command: $cmd" }
	RunCommand $shell $cmd '{"session_id":"t1"}' | Out-Null
	$sl = (Settings).statusLine.command
	$out = ''
	for ($i = 0; $i -lt 10 -and -not ($out -match 'mine'); $i++) {
		$out = RunCommand $shell $sl '{"session_id":"t1","model":{"id":"jev","display_name":"Jev (auto)"}}'
	}
	if (-not ($out -match 'mine' -and $out -match 'automodel:')) { Fail "statusline via ${shell}: $out" }
}

Step 'the hooks relaunch a dead proxy'
Stop-Process -Id ([int](Get-Content (Join-Path $stateDir 'proxy.pid'))) -Force
WaitFor { -not (Up) } 'proxy killed'
RunCommand 'bash' (HookCmd 'SessionStart') '{"session_id":"t2"}' | Out-Null
WaitFor { (Up).version -eq $ver } 'session-start hook relaunched the proxy'

Step 'install into a folder with spaces'
$env:AUTOMODEL_BIN_DIR = Join-Path $env:LOCALAPPDATA 'automodel test\bin dir'
& pwsh -NoProfile -NonInteractive -File (Join-Path $root 'install.ps1')
if ($LASTEXITCODE -ne 0) { Fail "install.ps1 (spaces) exited $LASTEXITCODE" }
$bin = Join-Path $env:AUTOMODEL_BIN_DIR 'automodel.exe'
$cmd = HookCmd 'SessionStart'
Write-Host "hook command: $cmd"
$shells = @('bash')
if (-not $cmd.StartsWith('"')) { $shells += 'pwsh' } # quoted only without 8.3 names: Git Bash only
foreach ($shell in $shells) {
	RunCommand $shell $cmd '{"session_id":"t3"}' | Out-Null
	RunCommand $shell (Settings).statusLine.command '{"session_id":"t3","model":{"id":"jev"}}' | Out-Null
}
& $bin doctor | Write-Host
if ($LASTEXITCODE -ne 0) { Fail 'doctor reported a failure (spaces)' }

Step 'uninstall'
& $bin uninstall
if ($LASTEXITCODE -ne 0) { Fail 'uninstall failed' }
WaitFor { -not (Up) } 'proxy stopped by uninstall'
$s = Get-Content -Raw $settings
if ($s.Contains('ANTHROPIC_BASE_URL')) { Fail 'settings not restored' }
if ((Settings).statusLine.command -ne 'echo mine') { Fail "statusline not restored: $s" }
if ((Get-ItemProperty $runKey).automodel) { Fail 'logon entry left' }
Write-Host 'install test passed (logon)' -ForegroundColor Green
