param(
    [string]$BaseUrl = "http://localhost:8080",
    [int]$Concurrency = 50
)

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

# Fire concurrent requests using background jobs
$jobs = 1..$Concurrency | ForEach-Object {
    $i = $_
    Start-Job -ScriptBlock {
        param($url, $voucherCode, $index)
        $body = @{ user_id = "user-$index"; idempotency_key = "burst-key-$voucherCode-$index" } | ConvertTo-Json
        try {
            $resp = Invoke-WebRequest -Uri "$url/vouchers/$voucherCode/redeem" -Method Post -Body $body -ContentType "application/json" -UseBasicParsing
            return [int]$resp.StatusCode
        } catch {
            if ($_.Exception.Response) {
                return [int]$_.Exception.Response.StatusCode
            }
            return 500
        }
    } -ArgumentList $BaseUrl, $code, $i
}

$results = $jobs | Wait-Job | Receive-Job
$jobs | Remove-Job -Force

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
