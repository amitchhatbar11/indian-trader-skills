# Installs the desk CLI (Indian Trader Skills) on Windows from the latest
# GitHub release:
#
#   irm https://raw.githubusercontent.com/amitchhatbar11/indian-trader-skills/main/install.ps1 | iex
#
# It checks the SHA-256 against the release's checksums.txt and installs to
# %LOCALAPPDATA%\desk (override with $env:DESK_INSTALL_DIR; pin a version
# with $env:DESK_VERSION = "v0.3.0").
$ErrorActionPreference = "Stop"

$repo = "amitchhatbar11/indian-trader-skills"
$dir = if ($env:DESK_INSTALL_DIR) { $env:DESK_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "desk" }
$version = if ($env:DESK_VERSION) { $env:DESK_VERSION } else { "latest" }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$name = "desk_windows_$arch"
$base = if ($version -eq "latest") { "https://github.com/$repo/releases/latest/download" } else { "https://github.com/$repo/releases/download/$version" }

$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "Downloading $name ($version)..."
    Invoke-WebRequest "$base/$name.zip" -OutFile "$tmp\$name.zip" -UseBasicParsing
    Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt" -UseBasicParsing

    $line = Get-Content "$tmp\checksums.txt" | Where-Object { $_ -match " $name\.zip$" }
    if (-not $line) { throw "no checksum for $name.zip" }
    $want = ($line -split "\s+")[0].ToLower()
    $got = (Get-FileHash "$tmp\$name.zip" -Algorithm SHA256).Hash.ToLower()
    if ($got -ne $want) { throw "checksum mismatch for $name.zip - not installing" }

    Expand-Archive "$tmp\$name.zip" -DestinationPath $tmp -Force
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    Copy-Item "$tmp\$name\desk.exe" "$dir\desk.exe" -Force

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (($userPath -split ";") -notcontains $dir) {
        [Environment]::SetEnvironmentVariable("Path", "$userPath;$dir", "User")
        Write-Host "Added $dir to your PATH (open a new terminal)."
    }
    Write-Host ("Installed: " + (& "$dir\desk.exe" version))
    Write-Host "Next: desk clock    (and see the README to set up a broker)"
}
finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
