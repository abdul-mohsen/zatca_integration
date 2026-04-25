# ============================================================
# Test the ZATCA daemon queue pipeline locally (no Docker)
#
# Prerequisites:
#   1. MySQL running on localhost:3306
#   2. Run: mysql -u root -p < scripts/test_setup.sql
#
# Usage: .\scripts\test_queue.ps1
# ============================================================

$ErrorActionPreference = "Stop"
$root = Split-Path $PSScriptRoot -Parent

# --- Config ---
$env:DB_USER     = "zatca_daemon"
$env:DB_PASS     = "test_pass_123"
$env:DB_HOST     = "127.0.0.1"
$env:DB_PORT     = "3306"
$env:MASTER_DB   = "zatca_master"
$env:ENCRYPT_KEY = "Z75tP56ou8HfdHr1PbBe7FpJV8U6YMzCGzGwb2ZthSU=" # 32-byte base64 key
$env:MONITOR_PASS = "testmon123"

Write-Host "=== Building daemon ===" -ForegroundColor Cyan
Push-Location $root
go build -o test_daemon.exe ./cmd/daemon
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "Build failed" }

Write-Host "=== Building natsdiag ===" -ForegroundColor Cyan
go build -o natsdiag.exe ./cmd/natsdiag
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "Build failed" }

# Clean up old NATS data
if (Test-Path ".\nats-data") { Remove-Item -Recurse -Force ".\nats-data" }

Write-Host "=== Starting daemon ===" -ForegroundColor Cyan
$daemon = Start-Process -FilePath ".\test_daemon.exe" `
    -ArgumentList "--nats-port=4222","--nats-monitor=0","--nats-data=.\nats-data" `
    -PassThru -NoNewWindow -RedirectStandardError "$root\daemon_stderr.log" -RedirectStandardOutput "$root\daemon_stdout.log"

Write-Host "  Daemon PID: $($daemon.Id)"
Write-Host "  Waiting 5 seconds for startup..."
Start-Sleep -Seconds 5

# Check if daemon is still running
if ($daemon.HasExited) {
    Write-Host "=== DAEMON CRASHED ===" -ForegroundColor Red
    Write-Host "--- stderr ---"
    Get-Content "$root\daemon_stderr.log"
    Write-Host "--- stdout ---"
    Get-Content "$root\daemon_stdout.log"
    Pop-Location
    throw "Daemon exited with code $($daemon.ExitCode)"
}

Write-Host "=== Daemon is running ===" -ForegroundColor Green
Write-Host "--- Daemon logs so far ---" -ForegroundColor Yellow
if (Test-Path "$root\daemon_stderr.log") { Get-Content "$root\daemon_stderr.log" }

Write-Host ""
Write-Host "=== Checking NATS stream info ===" -ForegroundColor Cyan
.\natsdiag.exe info

Write-Host ""
Write-Host "=== Publishing test onboard message ===" -ForegroundColor Cyan
.\natsdiag.exe pub zatca_test_db 1 123456

Write-Host ""
Write-Host "  Waiting 10 seconds for processing..."
Start-Sleep -Seconds 10

Write-Host ""
Write-Host "=== Daemon logs after publish ===" -ForegroundColor Yellow
if (Test-Path "$root\daemon_stderr.log") { Get-Content "$root\daemon_stderr.log" }

Write-Host ""
Write-Host "=== NATS stream info after publish ===" -ForegroundColor Cyan
.\natsdiag.exe info

Write-Host ""
Write-Host "=== Stopping daemon ===" -ForegroundColor Cyan
Stop-Process -Id $daemon.Id -Force -ErrorAction SilentlyContinue
Write-Host "  Daemon stopped."

# Cleanup
Remove-Item -Force ".\test_daemon.exe" -ErrorAction SilentlyContinue
Remove-Item -Force ".\natsdiag.exe" -ErrorAction SilentlyContinue

Pop-Location
Write-Host ""
Write-Host "=== DONE ===" -ForegroundColor Green
Write-Host "Check the logs above for 'Received message' and 'Processing onboard' lines."
