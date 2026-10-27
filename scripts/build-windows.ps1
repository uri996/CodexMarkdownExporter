[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $repoRoot 'dist'
$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGO = $env:CGO_ENABLED
Push-Location $repoRoot
try {
    # The source contains the single default version; packaging reads it too.
    $source = Get-Content -LiteralPath (Join-Path $repoRoot 'portable.go') -Raw -Encoding UTF8
    $match = [regex]::Match($source, 'var version = "(v[0-9]+\.[0-9]+\.[0-9]+)"')
    if (-not $match.Success) { throw 'Cannot read version from portable.go' }
    $releaseVersion = $match.Groups[1].Value
    New-Item -ItemType Directory -Path $dist -Force | Out-Null
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    $exe = Join-Path $dist 'Codex-Exporter.exe'
    # Default console subsystem; trim local source paths out of the executable.
    & go build -trimpath -buildvcs=false -ldflags '-s -w -X main.portableBuild=true' -o $exe .
    if ($LASTEXITCODE -ne 0) { throw "Windows build failed: $LASTEXITCODE" }
    $readme = Get-Content -LiteralPath (Join-Path $repoRoot 'docs\portable-README.txt') -Raw -Encoding UTF8
    $notices = Get-Content -LiteralPath (Join-Path $repoRoot 'THIRD_PARTY_NOTICES.txt') -Raw -Encoding UTF8
    [IO.File]::WriteAllText((Join-Path $dist 'README.txt'), "$readme`r`n`r`n$notices", (New-Object Text.UTF8Encoding($false)))
    Copy-Item -LiteralPath (Join-Path $repoRoot 'LICENSE') -Destination (Join-Path $dist 'LICENSE') -Force
    $zip = Join-Path $dist "Codex-Exporter-$releaseVersion-windows-amd64.zip"
    # Explicit allowlist: no source data, configuration or exports enter the ZIP.
    Compress-Archive -LiteralPath @($exe, (Join-Path $dist 'README.txt'), (Join-Path $dist 'LICENSE')) -DestinationPath $zip -Force
    $hash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText("$zip.sha256", "$hash  $([IO.Path]::GetFileName($zip))`n", [Text.Encoding]::ASCII)
    Write-Output "Built: $zip"
    Write-Output "SHA256: $hash"
}
finally {
    $env:GOOS = $oldGOOS
    $env:GOARCH = $oldGOARCH
    $env:CGO_ENABLED = $oldCGO
    Pop-Location
}
