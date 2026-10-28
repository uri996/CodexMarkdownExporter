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
    if ((Test-Path -LiteralPath $dist) -and ((Get-Item -LiteralPath $dist -Force).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw 'Refusing to build into a linked dist directory'
    }
    New-Item -ItemType Directory -Path $dist -Force | Out-Null
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    $exe = Join-Path $dist 'Codex-Markdown-Exporter.exe'
    # Default console subsystem; trim local source paths out of the executable.
    & go build -trimpath -buildvcs=false -ldflags '-s -w -X main.portableBuild=true' -o $exe .
    if ($LASTEXITCODE -ne 0) { throw "Windows build failed: $LASTEXITCODE" }
    # Copy verbatim: user instructions and complete legal notices are separate files.
    Copy-Item -LiteralPath (Join-Path $repoRoot 'docs\portable-README.txt') -Destination (Join-Path $dist 'README.txt') -Force
    Copy-Item -LiteralPath (Join-Path $repoRoot 'THIRD_PARTY_NOTICES.txt') -Destination (Join-Path $dist 'THIRD_PARTY_NOTICES.txt') -Force
    Copy-Item -LiteralPath (Join-Path $repoRoot 'LICENSE') -Destination (Join-Path $dist 'LICENSE') -Force
    $zip = Join-Path $dist "Codex-Markdown-Exporter-$releaseVersion-windows-amd64.zip"
    # Explicit allowlist: no source data, configuration or exports enter the ZIP.
    Compress-Archive -LiteralPath @($exe, (Join-Path $dist 'README.txt'), (Join-Path $dist 'LICENSE'), (Join-Path $dist 'THIRD_PARTY_NOTICES.txt')) -DestinationPath $zip -Force
    $hash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText("$zip.sha256", "$hash  $([IO.Path]::GetFileName($zip))`n", [Text.Encoding]::ASCII)
    # Remove only known legacy artifacts, after the replacement package succeeds.
    foreach ($legacyName in @('Codex-Exporter.exe', 'Codex-Exporter-v1.2.0-windows-amd64.zip', 'Codex-Exporter-v1.2.0-windows-amd64.zip.sha256')) {
        $legacyPath = [IO.Path]::GetFullPath((Join-Path $dist $legacyName))
        if (-not [string]::Equals([IO.Path]::GetDirectoryName($legacyPath), [IO.Path]::GetFullPath($dist), [StringComparison]::OrdinalIgnoreCase)) {
            throw 'Legacy artifact is outside dist'
        }
        if (Test-Path -LiteralPath $legacyPath) {
            $legacyItem = Get-Item -LiteralPath $legacyPath -Force
            if ($legacyItem.PSIsContainer -or ($legacyItem.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
                throw 'Refusing to remove a linked or non-file legacy artifact'
            }
            Remove-Item -LiteralPath $legacyPath -Force
        }
    }
    Write-Output "Built: $zip"
    Write-Output "SHA256: $hash"
}
finally {
    $env:GOOS = $oldGOOS
    $env:GOARCH = $oldGOARCH
    $env:CGO_ENABLED = $oldCGO
    Pop-Location
}
