$root   = $PSScriptRoot
$status = Join-Path $root 'build_status.txt'
"=== START $(Get-Date -Format o) ===" | Out-File -FilePath $status -Encoding UTF8

try {
    # 可选覆盖：GO_TOOLCHAIN（含 bin\ 的工具链根）、GOROOT、GOPATH
    if ($env:GO_TOOLCHAIN) {
        $env:PATH = "$(Join-Path $env:GO_TOOLCHAIN 'bin');$env:PATH"
    }
    if ($env:GOROOT) {
        # GOROOT 已由调用方设置，保持即可
    }
    if ($env:GOPATH) {
        $env:PATH = "$(Join-Path $env:GOPATH 'bin');$env:PATH"
    }

    $goCmd    = Get-Command go -ErrorAction SilentlyContinue
    $wailsCmd = Get-Command wails -ErrorAction SilentlyContinue

    "go on PATH = " + [bool]$goCmd | Out-File -FilePath $status -Append -Encoding UTF8
    "wails on PATH = " + [bool]$wailsCmd | Out-File -FilePath $status -Append -Encoding UTF8

    if (-not $goCmd) { throw "go not found on PATH (install Go, or set GO_TOOLCHAIN / GOROOT)" }
    if (-not $wailsCmd) { throw "wails not found on PATH (install wails, or set GOPATH)" }

    Set-Location $root
    $out = & wails build -s 2>&1
    $code = $LASTEXITCODE
    ($out | Out-String) | Out-File -FilePath (Join-Path $root 'build.log') -Encoding UTF8
    "EXITCODE=$code" | Out-File -FilePath $status -Append -Encoding UTF8
}
catch {
    "EXCEPTION: $($_.Exception.Message)" | Out-File -FilePath $status -Append -Encoding UTF8
    "TRACE: $($_.ScriptStackTrace)" | Out-File -FilePath $status -Append -Encoding UTF8
}
"=== END ===" | Out-File -FilePath $status -Append -Encoding UTF8