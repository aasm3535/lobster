# Lobster installer for Windows — one-liner (PowerShell):
#   irm https://yutugyutugyutug.com/install.ps1 | iex
# (the domain just redirects to raw.githubusercontent.com/aasm3535/lobster/main/install.ps1)
#
# Downloads the prebuilt binary from the latest GitHub release, or builds from source with Go.
$ErrorActionPreference = 'Stop'
$repo = 'aasm3535/lobster'

# --- looks: a coral dot leads each step, matching the TUI ---
function step($m) { Write-Host "  " -NoNewline; Write-Host "*" -ForegroundColor White -NoNewline; Write-Host " $m" }
function ok($m)   { Write-Host "  " -NoNewline; Write-Host "*" -ForegroundColor Green -NoNewline; Write-Host " $m" }
function warn($m) { Write-Host "  " -NoNewline; Write-Host "*" -ForegroundColor Red   -NoNewline; Write-Host " $m" }
function note($m) { Write-Host "    $m" -ForegroundColor DarkGray }

Write-Host ""
Write-Host "  lobster" -ForegroundColor Red -NoNewline; Write-Host "  ·  installer" -ForegroundColor DarkGray
Write-Host "  ------------------------" -ForegroundColor DarkGray
Write-Host ""

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$asset = "lobster_windows_$arch.exe"

$dir = Join-Path $env:LOCALAPPDATA 'lobster'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$out = Join-Path $dir 'lobster.exe'
$url = "https://github.com/$repo/releases/latest/download/$asset"

step "fetching $asset..."
$gotit = $false
try {
    Invoke-WebRequest -Uri $url -OutFile $out -UseBasicParsing
    if ((Get-Item $out).Length -gt 0) { $gotit = $true }
} catch { $gotit = $false }

if (-not $gotit) {
    step "no prebuilt binary - building from source..."
    if (Get-Command go -ErrorAction SilentlyContinue) {
        go install "github.com/$repo/cmd/lobster@latest"
        $gobin = (go env GOBIN); if (-not $gobin) { $gobin = Join-Path (go env GOPATH) 'bin' }
        $out = Join-Path $gobin 'lobster.exe'
        $dir = $gobin
    } else {
        warn "no release binary, and Go isn't installed."
        note "get Go at https://go.dev/dl then retry."
        throw "aborted"
    }
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$dir*") {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User')
    $env:Path = "$env:Path;$dir"
    note "added $dir to your PATH"
}
ok "installed to $out"
Write-Host ""

$ans = Read-Host "  Run setup now (configure + run in the background)? [Y/n]"
if ($ans -notmatch '^[Nn]') {
    & $out setup
} else {
    ok "done. next:"
    note "lobster setup     configure + run in the background"
    note "lobster tui       chat in your terminal"
}
