$token = (Get-Content "c:\ssda\chatGPT\clone\zatca_go_integration\.env" | Where-Object { $_ -match "^GITHUB_TOKEN=" }) -replace "^GITHUB_TOKEN=",""
$headers = @{
    Authorization = "token $token"
    Accept        = "application/vnd.github.v3.raw"
    "User-Agent"  = "PowerShell"
}
$base = "https://api.github.com/repos/abdul-mohsen/ifritah-go/contents"
$ref  = "dev"
$outDir = "c:\ssda\chatGPT\clone\zatca_go_integration\output\backend"

if (-not (Test-Path $outDir)) { New-Item -ItemType Directory -Path $outDir -Force | Out-Null }

$files = @(
    "pkg/db/schema/schema.sql",
    "pkg/db/queries/bill.sql",
    "pkg/db/queries/credit.sql",
    "pkg/db/queries/client.sql",
    "pkg/db/queries/company.sql",
    "pkg/db/queries/product.sql",
    "pkg/model/bill.go",
    "pkg/model/product.go",
    "pkg/handlers/bill.go",
    "pkg/handlers/credit.go",
    "pkg/handlers/branch.go",
    "pkg/handlers/store.go",
    "pkg/handlers/company.go",
    "pkg/handlers/handler.go",
    "pkg/db/db.go"
)

foreach ($f in $files) {
    $outFile = Join-Path $outDir ($f -replace "/","_")
    Write-Host "Fetching $f ..."
    try {
        Invoke-RestMethod -Uri "$base/$($f)?ref=$ref" -Headers $headers -OutFile $outFile
        Write-Host "  -> $outFile"
    } catch {
        Write-Host "  FAILED: $_"
    }
}
Write-Host "Done."
