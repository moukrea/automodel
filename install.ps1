# automodel installer for Windows (PowerShell 5.1 or 7):
#   irm https://raw.githubusercontent.com/moukrea/automodel/main/install.ps1 | iex
# Installs the latest release in %LOCALAPPDATA%\automodel\bin (AUTOMODEL_BIN_DIR),
# adds it to the user PATH, wires it into Claude Code (`automodel install`)
# and asks for the OpenRouter key (or takes $env:OPENROUTER_API_KEY). Pin a
# version with $env:AUTOMODEL_VERSION = 'v0.1.0'.
# Tests: AUTOMODEL_BASE_URL (a URL, a file:// URL or a folder) replaces the
# release download URL; the zip and checksums.txt must be there,
# AUTOMODEL_VERSION is required.

& {
	$ErrorActionPreference = 'Stop'
	$ProgressPreference = 'SilentlyContinue' # Invoke-WebRequest is slow with it
	$repo = 'moukrea/automodel'
	$binDir = if ($env:AUTOMODEL_BIN_DIR) { $env:AUTOMODEL_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'automodel\bin' }

	function Say($msg) { Write-Host $msg -ForegroundColor Cyan }
	# `throw`, not `exit`: under `irm | iex`, exit would close the window.
	function Fail($msg) { throw "automodel install: $msg" }

	# GitHub needs TLS 1.2, which Windows PowerShell 5.1 may not enable.
	[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

	$arch = try { [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() } catch { $env:PROCESSOR_ARCHITECTURE }
	switch -Regex ($arch) {
		'^(X64|AMD64)$' { $arch = 'amd64' }
		'^(Arm64|ARM64)$' { $arch = 'arm64' }
		default { Fail "unsupported CPU: $arch" }
	}

	$tag = $env:AUTOMODEL_VERSION
	if ($env:AUTOMODEL_BASE_URL -and -not $tag) { Fail 'AUTOMODEL_BASE_URL needs AUTOMODEL_VERSION' }
	if (-not $tag) {
		$tag = (Invoke-RestMethod -UseBasicParsing -Headers @{ Accept = 'application/vnd.github+json' } "https://api.github.com/repos/$repo/releases/latest").tag_name
	}
	if (-not $tag) { Fail 'could not find the latest release' }
	$ver = $tag -replace '^v', ''
	$file = "automodel_${ver}_windows_${arch}.zip"
	$base = if ($env:AUTOMODEL_BASE_URL) { $env:AUTOMODEL_BASE_URL.TrimEnd('/') } else { "https://github.com/$repo/releases/download/$tag" }

	function Fetch($name, $dest) {
		if ($base -match '^https?://') {
			Invoke-WebRequest -UseBasicParsing "$base/$name" -OutFile $dest
		} else {
			$dir = if ($base -match '^file:') { ([Uri]$base).LocalPath } else { $base }
			Copy-Item -LiteralPath (Join-Path $dir $name) -Destination $dest
		}
	}

	$tmp = Join-Path ([IO.Path]::GetTempPath()) ("automodel-" + [Guid]::NewGuid())
	New-Item -ItemType Directory -Path $tmp | Out-Null
	try {
		Say "Downloading automodel $tag (windows/$arch)"
		Fetch $file (Join-Path $tmp $file)
		Fetch 'checksums.txt' (Join-Path $tmp 'checksums.txt')
		$want = ''
		foreach ($line in Get-Content (Join-Path $tmp 'checksums.txt')) {
			$f = $line -split '\s+'
			if ($f.Count -ge 2 -and $f[1] -eq $file) { $want = $f[0].ToLower() }
		}
		$got = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $tmp $file)).Hash.ToLower()
		if (-not $want -or $want -ne $got) { Fail "checksum mismatch for $file" }
		Expand-Archive -LiteralPath (Join-Path $tmp $file) -DestinationPath (Join-Path $tmp 'x') -Force
		$new = Get-ChildItem -LiteralPath (Join-Path $tmp 'x') -Recurse -Filter 'automodel.exe' | Select-Object -First 1
		if (-not $new) { Fail "automodel.exe not found in $file" }

		New-Item -ItemType Directory -Force -Path $binDir | Out-Null
		$exe = Join-Path $binDir 'automodel.exe'
		if (Test-Path -LiteralPath $exe) {
			# A running proxy keeps its binary open: Windows can't overwrite
			# it, but can rename it (removed by the next proxy start).
			Remove-Item -LiteralPath "$exe.old" -Force -ErrorAction SilentlyContinue
			Move-Item -LiteralPath $exe -Destination "$exe.old" -Force
		}
		Copy-Item -LiteralPath $new.FullName -Destination $exe
		Say "Installed $exe"
	} finally {
		Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
	}

	# User PATH, for new terminals and for this one.
	$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
	$parts = @($userPath -split ';' | Where-Object { $_ })
	if ($parts -notcontains $binDir) {
		[Environment]::SetEnvironmentVariable('Path', (@($parts) + $binDir) -join ';', 'User')
		Say "Added $binDir to your PATH (new terminals)"
	}
	if (@($env:Path -split ';') -notcontains $binDir) { $env:Path = "$env:Path;$binDir" }

	$env:AUTOMODEL_INSTALLER = '1'
	try {
		& $exe install
		if ($LASTEXITCODE -ne 0) { Fail "automodel install failed (exit $LASTEXITCODE)" }
	} finally {
		Remove-Item Env:AUTOMODEL_INSTALLER -ErrorAction SilentlyContinue
	}

	$cfg = if ($env:AUTOMODEL_CONFIG) { $env:AUTOMODEL_CONFIG }
	elseif ($env:XDG_CONFIG_HOME) { Join-Path $env:XDG_CONFIG_HOME 'automodel\config.toml' }
	else { Join-Path $HOME '.config\automodel\config.toml' }
	function HasKey { (Test-Path -LiteralPath $cfg) -and (Select-String -LiteralPath $cfg -Pattern '^openrouter_api_key = ".+"' -Quiet) }

	if ($env:OPENROUTER_API_KEY) {
		$env:OPENROUTER_API_KEY | & $exe key set
	} elseif (-not (HasKey) -and [Environment]::UserInteractive -and -not [Console]::IsInputRedirected) {
		try {
			$secure = Read-Host 'OpenRouter API key for Jev (hidden, empty to skip)' -AsSecureString
			$bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
			try { $key = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr) }
			finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
			if ($key) { $key | & $exe key set }
		} catch {
			# -NonInteractive or no console: skip, as without a terminal.
		}
	}

	if (-not (HasKey) -and -not $env:OPENROUTER_API_KEY) {
		Say 'No OpenRouter key yet: until you run `automodel key set`, every prompt uses the default tier.'
	}
	Say 'Done. In Claude Code, open /model and pick "Jev (auto)". `automodel doctor` checks the setup.'
}
