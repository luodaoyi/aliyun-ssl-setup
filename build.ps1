$root   = 'C:\Users\asura\WorkBuddy\2026-08-25-11-39-38\sslpanel-app'
$status = Join-Path $root 'build_status.txt'
"=== START $(Get-Date -Format o) ===" | Out-File -FilePath $status -Encoding UTF8

try {
    $goBin  = 'C:\Users\asura\.workbuddy\binaries\go\go\bin'
    $gopath = 'C:\Users\asura\.workbuddy\binaries\go\gopath'
    $wails  = Join-Path $gopath 'bin\wails.exe'
    $goexe  = Join-Path $goBin 'go.exe'

    "goexe exists = " + (Test-Path $goexe) | Out-File -FilePath $status -Append -Encoding UTF8
    "wails exists = " + (Test-Path $wails) | Out-File -FilePath $status -Append -Encoding UTF8

    $env:PATH   = "$goBin;$gopath\bin;$env:PATH"
    $env:GOPATH = $gopath
    $env:GOROOT = 'C:\Users\asura\.workbuddy\binaries\go\go'

    Set-Location $root
    $out = & $wails build -s 2>&1
    $code = $LASTEXITCODE
    ($out | Out-String) | Out-File -FilePath (Join-Path $root 'build.log') -Encoding UTF8
    "EXITCODE=$code" | Out-File -FilePath $status -Append -Encoding UTF8
}
catch {
    "EXCEPTION: $($_.Exception.Message)" | Out-File -FilePath $status -Append -Encoding UTF8
    "TRACE: $($_.ScriptStackTrace)" | Out-File -FilePath $status -Append -Encoding UTF8
}
"=== END ===" | Out-File -FilePath $status -Append -Encoding UTF8
