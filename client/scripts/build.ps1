$ErrorActionPreference = 'Stop'

$clientRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
$outputDirectory = Join-Path $clientRoot 'bin'
$requiredGarbleVersion = 'v0.17.0'
$targets = @(
    [PSCustomObject]@{
        OS           = 'linux'
        Architecture = 'amd64'
        Output       = 'portlistener2dns-linux-amd64'
    }
    [PSCustomObject]@{
        OS           = 'linux'
        Architecture = 'arm64'
        Output       = 'portlistener2dns-linux-arm64'
    }
    [PSCustomObject]@{
        OS           = 'windows'
        Architecture = 'amd64'
        Output       = 'portlistener2dns-windows-amd64.exe'
    }
)

$goPath = ((& go env GOPATH).Trim() -split [IO.Path]::PathSeparator)[0]
$garbleCommand = Get-Command garble -CommandType Application -ErrorAction SilentlyContinue |
    Select-Object -First 1
if ($null -eq $garbleCommand) {
    $garbleBinaryName = if ([IO.Path]::DirectorySeparatorChar -eq '\') {
        'garble.exe'
    } else {
        'garble'
    }
    $candidate = Join-Path (Join-Path $goPath 'bin') $garbleBinaryName
    if (Test-Path -LiteralPath $candidate) {
        $garbleExecutable = $candidate
    } else {
        throw "未找到 Garble。请先执行: go install mvdan.cc/garble@$requiredGarbleVersion"
    }
} else {
    $garbleExecutable = $garbleCommand.Source
}

$garbleVersionInfo = @(& $garbleExecutable version)
if ($LASTEXITCODE -ne 0) {
    throw "无法读取 Garble 版本，退出码: $LASTEXITCODE"
}
$garbleVersionMatch = [regex]::Match(
    ($garbleVersionInfo -join "`n"),
    '(?m)^mvdan\.cc/garble\s+(v[^\s]+)'
)
if (
    -not $garbleVersionMatch.Success -or
    $garbleVersionMatch.Groups[1].Value -ne $requiredGarbleVersion
) {
    $actualVersion = if ($garbleVersionMatch.Success) {
        $garbleVersionMatch.Groups[1].Value
    } else {
        '未知'
    }
    throw "Garble 版本必须为 $requiredGarbleVersion，当前为 $actualVersion。请执行: go install mvdan.cc/garble@$requiredGarbleVersion"
}

New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
Push-Location $clientRoot
try {
    & go test ./...
    if ($LASTEXITCODE -ne 0) {
        throw "Go 测试失败，退出码: $LASTEXITCODE"
    }

    $originalGoOS = [Environment]::GetEnvironmentVariable('GOOS', 'Process')
    $originalGoArch = [Environment]::GetEnvironmentVariable('GOARCH', 'Process')
    $originalCGO = [Environment]::GetEnvironmentVariable('CGO_ENABLED', 'Process')
    try {
        $env:CGO_ENABLED = '0'
        foreach ($target in $targets) {
            $env:GOOS = $target.OS
            $env:GOARCH = $target.Architecture
            $outputPath = Join-Path $outputDirectory $target.Output

            Write-Host "正在构建 $($target.OS)/$($target.Architecture)..."
            & $garbleExecutable -literals -tiny -seed=random build `
                -trimpath '-ldflags=-s -w -buildid=' `
                -o $outputPath `
                '.\cmd\portlistener2dns'
            if ($LASTEXITCODE -ne 0) {
                throw "$($target.OS)/$($target.Architecture) Garble 构建失败，退出码: $LASTEXITCODE"
            }
        }
    } finally {
        [Environment]::SetEnvironmentVariable('GOOS', $originalGoOS, 'Process')
        [Environment]::SetEnvironmentVariable('GOARCH', $originalGoArch, 'Process')
        [Environment]::SetEnvironmentVariable('CGO_ENABLED', $originalCGO, 'Process')
    }

    $garbleVersionInfo | Write-Output
    Get-ChildItem -LiteralPath $outputDirectory |
        Select-Object Name, Length, LastWriteTime |
        Format-Table -AutoSize
} finally {
    Pop-Location
}
