# Lobster installer for Windows — one-liner (PowerShell):
#   irm https://raw.githubusercontent.com/aasm3535/lobster/main/install.ps1 | iex
#
# Downloads the prebuilt binary from the latest GitHub release, or builds from source with Go.
$ErrorActionPreference = 'Stop'
$repo = 'aasm3535/lobster'

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$asset = "lobster_windows_$arch.exe"

$dir = Join-Path $env:LOCALAPPDATA 'lobster'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$out = Join-Path $dir 'lobster.exe'
$url = "https://github.com/$repo/releases/latest/download/$asset"

Write-Host "Installing lobster ($asset)..."
$ok = $false
try {
    Invoke-WebRequest -Uri $url -OutFile $out -UseBasicParsing
    if ((Get-Item $out).Length -gt 0) { $ok = $true }
} catch { $ok = $false }

if (-not $ok) {
    Write-Host "No prebuilt binary - building from source..."
    if (Get-Command go -ErrorAction SilentlyContinue) {
        go install "github.com/$repo/cmd/lobster@latest"
        $gobin = (go env GOBIN); if (-not $gobin) { $gobin = Join-Path (go env GOPATH) 'bin' }
        $out = Join-Path $gobin 'lobster.exe'
        $dir = $gobin
    } else {
        throw "No release binary and Go isn't installed. Get Go at https://go.dev/dl then retry."
    }
}

# Add the install dir to the user PATH if it's not already there.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$dir*") {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User')
    Write-Host "Added $dir to your PATH (restart the terminal to pick it up)."
}
Write-Host "Installed to $out"
Write-Host "Next: lobster setup   (or: lobster tui)"
