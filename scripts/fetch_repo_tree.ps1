$token = (Get-Content "c:\ssda\chatGPT\clone\zatca_go_integration\.env" | Where-Object { $_ -match "^GITHUB_TOKEN=" }) -replace "^GITHUB_TOKEN=",""
$headers = @{
    Authorization = "token $token"
    Accept        = "application/vnd.github.v3+json"
    "User-Agent"  = "PowerShell"
}
$base = "https://api.github.com/repos/abdul-mohsen/ifritah-go"
$ref  = "dev"

# Get repo tree recursively
$tree = Invoke-RestMethod -Uri "$base/git/trees/$($ref)?recursive=1" -Headers $headers
$tree.tree | Where-Object { $_.type -eq "blob" } | ForEach-Object { $_.path } | Sort-Object
