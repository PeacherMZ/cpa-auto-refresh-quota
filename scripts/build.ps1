[CmdletBinding()]
param(
    [string]$TargetOS,
    [string]$TargetArch,
    [string]$OutputDir = "dist",
    [string]$Version,
    [string]$GoBinary = "go"
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($TargetOS)) {
    $TargetOS = (& $GoBinary env GOOS).Trim()
}
if ([string]::IsNullOrWhiteSpace($TargetArch)) {
    $TargetArch = (& $GoBinary env GOARCH).Trim()
}

$extension = switch ($TargetOS) {
    "windows" { ".dll" }
    "darwin" { ".dylib" }
    default { ".so" }
}

$normalizedVersion = "$Version".Trim()
if ($normalizedVersion.StartsWith("v")) {
    $normalizedVersion = $normalizedVersion.Substring(1)
}
if ($normalizedVersion -and $normalizedVersion -notmatch '^[0-9][0-9A-Za-z.+-]*$') {
    throw "版本号格式无效: $Version"
}

if (-not [System.IO.Path]::IsPathRooted($OutputDir)) {
    $OutputDir = Join-Path $repoRoot $OutputDir
}
New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

$artifactName = "cpa-auto-refresh-quota"
if ($normalizedVersion) {
    $artifactName += "-v$normalizedVersion"
}
$artifact = Join-Path $OutputDir ($artifactName + $extension)
$header = [System.IO.Path]::ChangeExtension($artifact, ".h")

$ldflags = "-s -w"
if ($normalizedVersion) {
    $ldflags += " -X main.pluginVersion=$normalizedVersion"
}

$previousCGO = $env:CGO_ENABLED
$previousGOOS = $env:GOOS
$previousGOARCH = $env:GOARCH

try {
    $env:CGO_ENABLED = "1"
    $env:GOOS = $TargetOS
    $env:GOARCH = $TargetArch

    Push-Location $repoRoot
    try {
        $buildArguments = @(
            "build",
            "-buildvcs=false",
            "-trimpath",
            "-ldflags=$ldflags",
            "-buildmode=c-shared",
            "-o", $artifact,
            "./cmd/cpa-auto-refresh-quota"
        )
        & $GoBinary @buildArguments
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    $env:CGO_ENABLED = $previousCGO
    $env:GOOS = $previousGOOS
    $env:GOARCH = $previousGOARCH
}

Remove-Item -LiteralPath $header -Force -ErrorAction SilentlyContinue
Write-Host "Built $artifact for $TargetOS/$TargetArch"
