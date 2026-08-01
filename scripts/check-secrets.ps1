[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $repoRoot
try {
    & git rev-parse --is-inside-work-tree *> $null
    if ($LASTEXITCODE -ne 0) {
        throw "当前目录不是 Git 工作区，请先执行 git init。"
    }

    $patterns = @(
        'AKIA[0-9A-Z]{16}',
        'gh[pousr]_[A-Za-z0-9]{20,}',
        'sk-[A-Za-z0-9_-]{24,}',
        'AIza[0-9A-Za-z_-]{35}',
        'xox[baprs]-[A-Za-z0-9-]{20,}',
        '-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----'
    )

    $found = $false
    foreach ($pattern in $patterns) {
        $grepArguments = @(
            "grep", "--no-index", "--exclude-standard", "-n", "-I", "-E",
            "--", $pattern, "--", ".",
            ":(exclude)scripts/check-secrets.ps1",
            ":(exclude)scripts/check-secrets.sh"
        )
        $matches = & git @grepArguments 2>$null
        if ($LASTEXITCODE -eq 0) {
            $matches | Write-Output
            $found = $true
        }
        elseif ($LASTEXITCODE -ne 1) {
            throw "git grep 执行失败，退出码: $LASTEXITCODE"
        }
    }

    $sensitiveFiles = & git ls-files --cached --others --exclude-standard | Where-Object {
        $_ -match '^config\.ya?ml$' -or
        $_ -match '^config\..*\.local\.ya?ml$' -or
        $_ -match '(^|/)(\.env(?:\..+)?|id_(?:rsa|ed25519)(?:\.pub)?|auths?/.*\.json)$' -or
        $_ -match '\.(pem|key|p12|pfx)$'
    } | Where-Object {
        $_ -notmatch '(^|/)\.env\.example$'
    }

    if ($sensitiveFiles) {
        Write-Output "发现疑似敏感文件名："
        $sensitiveFiles | Write-Output
        $found = $true
    }

    if ($found) {
        throw "敏感信息检查失败。请确认命中内容均为无害占位符，否则立即移除并轮换相关凭据。"
    }

    Write-Output "敏感信息检查通过。"
}
finally {
    Pop-Location
}
