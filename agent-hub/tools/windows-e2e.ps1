# End-to-end test of the real AgentHub.exe on Windows (used by CI on windows-latest).
# Usage (pwsh, from agent-hub/):  ./tools/windows-e2e.ps1 -Exe dist/AgentHub.exe -Out e2e-out
param(
    [string]$Exe = "dist/AgentHub.exe",
    [string]$Out = "e2e-out"
)
$ErrorActionPreference = 'Stop'
$Exe = (Resolve-Path $Exe).Path
New-Item -ItemType Directory -Force $Out | Out-Null
$Out = (Resolve-Path $Out).Path
$tmp = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { $env:TEMP }
$script:n = 0
$procs = @()

function Ok($cond, [string]$msg) {
    $script:n++
    if (-not $cond) { throw "FAIL #$($script:n): $msg" }
    Write-Host "  ok $($script:n) - $msg"
}
function Wait-Until([scriptblock]$cond, [int]$sec = 20) {
    $deadline = (Get-Date).AddSeconds($sec)
    while ((Get-Date) -lt $deadline) {
        try { if (& $cond) { return $true } } catch {}
        Start-Sleep -Milliseconds 300
    }
    return $false
}
function Api([int]$port, [string]$method, [string]$path, $body, [hashtable]$headers = @{ 'X-Agent-Hub' = '1' }) {
    $p = @{ Method = $method; Uri = "http://127.0.0.1:$port$path"; Headers = $headers; ContentType = 'application/json; charset=utf-8' }
    if ($null -ne $body) { $p.Body = [Text.Encoding]::UTF8.GetBytes(($body | ConvertTo-Json -Depth 10 -Compress)) }
    Invoke-RestMethod @p
}
function Status([int]$port, [string]$method, [string]$path, [hashtable]$headers) {
    (Invoke-WebRequest -Method $method -Uri "http://127.0.0.1:$port$path" -Headers $headers -Body '{}' -ContentType 'application/json' -SkipHttpErrorCheck).StatusCode
}
function Screenshot([string]$file) {
    try {
        Add-Type -AssemblyName System.Windows.Forms, System.Drawing
        $b = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
        $bmp = New-Object System.Drawing.Bitmap $b.Width, $b.Height
        $g = [System.Drawing.Graphics]::FromImage($bmp)
        $g.CopyFromScreen($b.Location, [System.Drawing.Point]::Empty, $b.Size)
        $bmp.Save($file)
        Write-Host "  (screenshot saved: $file)"
    } catch { Write-Host "  (screenshot failed: $_)" }
}

try {
    Write-Host "== 1. server mode (--no-window)"
    $data1 = Join-Path $tmp 'ah-e2e-1'
    Remove-Item -Recurse -Force $data1 -ErrorAction SilentlyContinue
    $p1 = Start-Process $Exe -ArgumentList '--no-window', '--data', "`"$data1`"", '--port', '17401' -PassThru
    $procs += $p1
    Ok (Wait-Until { (Api 17401 GET /api/ping).app -eq 'agent-hub' }) 'exe starts and answers /api/ping'
    $st = Api 17401 GET /api/state
    Ok ($st.accounts.Count -eq 2) 'first run seeds 2 example accounts'
    Ok (Test-Path (Join-Path $data1 'data.json')) 'data.json written to data dir'
    Ok (Test-Path (Join-Path $data1 'agent-hub.log')) 'log file written'
    Ok ($st.codexHome -like "$env:USERPROFILE*") "default CODEX_HOME is under the user profile ($($st.codexHome))"

    Write-Host "== 2. security"
    Ok ((Status 17401 POST /api/accounts @{ 'X-Agent-Hub' = '1'; Origin = 'http://evil.example' }) -eq 403) 'cross-site POST rejected'
    Ok ((Status 17401 POST /api/accounts @{}) -eq 403) 'POST without app header rejected'
    Ok ((Status 17401 POST /api/report @{ 'X-Token' = 'wrong' }) -eq 401) 'report with wrong token rejected'

    Write-Host "== 3. report API"
    $r = Api 17401 POST /api/report @{ account = 'Claude 1'; five_hour = @{ used = 23; resets_in_seconds = 7200 }; weekly = @{ left = 68 } } @{ 'X-Token' = $st.settings.token }
    Ok ($r.ok -and $r.usage.fiveHour.left -eq 77 -and $r.usage.weekly.left -eq 68) 'report stored (77% / 68%)'

    Write-Host "== 4. launch a console with the account's config dir"
    $cfg = Join-Path $tmp 'ah-claude-cfg'
    $work = Join-Path $tmp 'ah-work'
    $envFile = Join-Path $tmp 'ah-launch.txt'
    Remove-Item -Recurse -Force $cfg, $envFile -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force $work | Out-Null
    $st = Api 17401 POST /api/accounts @{
        name = 'Launch Test'; agent = '阿测'; provider = 'claude'; configDir = $cfg; workDir = $work
        launchCmd = "(echo %CLAUDE_CONFIG_DIR%& cd)>$envFile & ping -n 4 127.0.0.1 >nul & exit"
    }
    $acc = $st.accounts | Where-Object name -eq 'Launch Test'
    $r = Api 17401 POST "/api/accounts/$($acc.id)/launch"
    Ok ($r.running.($acc.id) -eq $true) 'launch reports the account as running'
    Ok (Test-Path $cfg) 'config dir created before launch'
    Ok (Wait-Until { Test-Path $envFile } 15) 'launched console ran the command'
    $lines = @(Get-Content $envFile | ForEach-Object { $_.Trim() })
    Ok ($lines[0] -eq $cfg) "CLAUDE_CONFIG_DIR passed to the console ($($lines[0]))"
    Ok ($lines[1] -eq $work) "console started in the work dir ($($lines[1]))"
    Ok (Wait-Until { -not (Api 17401 GET /api/state).running.($acc.id) } 30) 'running flag clears when the console closes'

    Write-Host "== 5. UI end-to-end in Microsoft Edge"
    node (Join-Path $PSScriptRoot 'ui-e2e.js') 'http://127.0.0.1:17401' $Out msedge
    Ok ($LASTEXITCODE -eq 0) 'UI E2E passed in Edge'

    Write-Host "== 6. single instance"
    $p2 = Start-Process $Exe -ArgumentList '--no-window', '--data', "`"$data1`"", '--port', '17401' -PassThru
    Ok ($p2.WaitForExit(10000)) 'second copy on the same port exits'
    Ok (-not $p1.HasExited) 'first copy keeps running'
    Stop-Process -Id $p1.Id -Force

    Write-Host "== 7. window mode: Edge app window and exit on close"
    $data2 = Join-Path $tmp 'ah-e2e-2'
    Remove-Item -Recurse -Force $data2 -ErrorAction SilentlyContinue
    $p3 = Start-Process $Exe -ArgumentList '--data', "`"$data2`"", '--port', '17402' -PassThru
    $procs += $p3
    Ok (Wait-Until { (Api 17402 GET /api/ping).app -eq 'agent-hub' }) 'window mode server up'
    $script:edge = $null
    Ok (Wait-Until {
            $script:edge = Get-CimInstance Win32_Process -Filter "Name='msedge.exe'" |
                Where-Object { $_.CommandLine -like '*--app=http://127.0.0.1:17402/*' -and $_.CommandLine -notlike '*--type=*' } |
                Select-Object -First 1
            $null -ne $script:edge
        } 30) 'Edge opened in --app mode'
    Ok (Wait-Until { (Get-Process -Id $script:edge.ProcessId -ErrorAction SilentlyContinue).MainWindowHandle -ne 0 } 20) 'app window is visible'
    Start-Sleep -Seconds 8
    Screenshot (Join-Path $Out 'window-mode.png')
    Ok (-not $p3.HasExited) 'Agent Hub stays running while the window is open'
    Stop-Process -Id $script:edge.ProcessId -Force
    Ok ($p3.WaitForExit(20000)) 'closing the window exits Agent Hub'

    Write-Host "WINDOWS E2E PASSED ($($script:n) checks)"
} finally {
    foreach ($p in $procs) { if (-not $p.HasExited) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue } }
    foreach ($d in @('ah-e2e-1', 'ah-e2e-2')) {
        $log = Join-Path (Join-Path $tmp $d) 'agent-hub.log'
        if (Test-Path $log) { Copy-Item $log (Join-Path $Out "$d.log") }
    }
}
