param(
    [string]$BaseUrl = "http://localhost:8080",
    [int]$Concurrency = 50
)

# Ensure System.Net.Http is loaded in Windows PowerShell 5.1 & PowerShell 7
Add-Type -AssemblyName System.Net.Http

$code = "burst-ps-" + [DateTimeOffset]::Now.ToUnixTimeSeconds()
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "   Voucher Service Burst Gate (PowerShell)" -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "Target URL:  $BaseUrl"
Write-Host "Concurrency: $Concurrency"
Write-Host "Voucher Code:$code"
Write-Host ""

# Create voucher
$createBody = @{ code = $code; max_redemptions = 1 } | ConvertTo-Json
$createResp = Invoke-RestMethod -Uri "$BaseUrl/vouchers" -Method Post -Body $createBody -ContentType "application/json"
Write-Host "Voucher created: remaining = $($createResp.remaining)"
Write-Host "Firing $Concurrency parallel requests..." -ForegroundColor Cyan

$client = [System.Net.Http.HttpClient]::new()
$client.Timeout = [TimeSpan]::FromSeconds(15)

$tasks = 1..$Concurrency | ForEach-Object {
    $index = $_
    $uri = "$BaseUrl/vouchers/$code/redeem"
    $json = "{`"user_id`":`"user-$index`",`"idempotency_key`":`"burst-key-$code-$index`"}"
    
    [System.Threading.Tasks.Task]::Run([Func[int]]{
        try {
            $content = [System.Net.Http.StringContent]::new($json, [System.Text.Encoding]::UTF8, "application/json")
            $resp = $client.PostAsync($uri, $content).GetAwaiter().GetResult()
            return [int]$resp.StatusCode
        } catch {
            return 500
        }
    })
}

[System.Threading.Tasks.Task]::WaitAll($tasks)
$results = $tasks | ForEach-Object { $_.Result }
$client.Dispose()

$successes = ($results | Where-Object { $_ -eq 200 }).Count
$exhausted = ($results | Where-Object { $_ -eq 422 }).Count
$errors    = ($results | Where-Object { $_ -ge 500 }).Count

Write-Host ""
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "              BURST RESULTS               " -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "Granted Redemptions (HTTP 200): $successes" -ForegroundColor Green
Write-Host "Clean Rejections     (HTTP 422): $exhausted" -ForegroundColor Yellow
Write-Host "Server Errors        (HTTP 5xx): $errors" -ForegroundColor Red
Write-Host ""

if ($successes -eq 1 -and $errors -eq 0) {
    Write-Host "🎉 SUCCESS: Exactly 1 redemption granted, 0 server errors!" -ForegroundColor Green
} else {
    Write-Host "❌ FAIL: Expected 1 success and 0 errors." -ForegroundColor Red
}
