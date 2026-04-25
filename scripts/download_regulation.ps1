$pdfUrl = "https://zatca.gov.sa/en/E-Invoicing/Introduction/LawsAndRegulations/Documents/E-invoicing%20Regulation%20EN.pdf"
$outFile = "$PSScriptRoot\..\output\zatca_regulation.pdf"

[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$wc = New-Object System.Net.WebClient
$wc.DownloadFile($pdfUrl, $outFile)
Write-Host "Downloaded: $((Get-Item $outFile).Length) bytes"

# Try basic text extraction from PDF
$bytes = [System.IO.File]::ReadAllBytes($outFile)
$text = [System.Text.Encoding]::UTF8.GetString($bytes)

# Extract readable text between stream markers
$matches = [regex]::Matches($text, '(?s)BT\s(.*?)ET')
$extracted = ""
foreach ($m in $matches) {
    $lines = $m.Groups[1].Value -split "`n"
    foreach ($line in $lines) {
        if ($line -match '\((.+?)\)\s*Tj') {
            $extracted += $Matches[1] + " "
        }
        if ($line -match '<([0-9A-Fa-f]+)>\s*Tj') {
            # hex encoded text - skip for now
        }
    }
}

if ($extracted.Length -gt 100) {
    Write-Host "`n--- Extracted Text (looking for penalty/violation/fine) ---"
    $lines = $extracted -split '\.' 
    foreach ($l in $lines) {
        if ($l -match 'penalt|violat|fine|sanction|tamper|hash|sequence|compli') {
            Write-Host $l.Trim()
        }
    }
    Write-Host "`n--- Full text length: $($extracted.Length) chars ---"
} else {
    Write-Host "Could not extract text from PDF (likely compressed streams)"
    Write-Host "Text length: $($extracted.Length)"
}
