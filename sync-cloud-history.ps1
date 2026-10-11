# sync-cloud-history.ps1
# Pulls the cloud "data" branch's price-history days into ./history, without
# overwriting any day you already have, so your own scans are always kept.
# Safe to re-run anytime. Does not touch prices.json or your live data.
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

Write-Host "Fetching the data branch from GitHub..."
git fetch origin data | Out-Null

$tmp = Join-Path $PSScriptRoot "cloud-tmp"
$tar = Join-Path $PSScriptRoot "cloud-tmp.tar"
Remove-Item -Recurse -Force $tmp, $tar -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $tmp | Out-Null

# Export the data branch to a tar file, then extract it (a file avoids the
# binary-pipe corruption you'd get piping git archive straight into tar).
git archive --format=tar -o $tar origin/data
tar -xf $tar -C $tmp
Remove-Item $tar

# Decompress the raw .gz files so the app can read them.
Get-ChildItem (Join-Path $tmp "history\raw") -Filter *.gz -ErrorAction SilentlyContinue | ForEach-Object {
    $out = $_.FullName -replace '\.gz$', ''
    $in  = [System.IO.File]::OpenRead($_.FullName)
    $gz  = New-Object System.IO.Compression.GZipStream($in, [System.IO.Compression.CompressionMode]::Decompress)
    $dst = [System.IO.File]::Create($out)
    $gz.CopyTo($dst); $dst.Close(); $gz.Close(); $in.Close()
    Remove-Item $_.FullName
}

# Merge into ./history, never overwriting a file you already have.
$added = 0
foreach ($sub in "daily", "raw", "flips") {
    $src = Join-Path $tmp "history\$sub"
    if (Test-Path $src) {
        $destDir = Join-Path $PSScriptRoot "history\$sub"
        New-Item -ItemType Directory -Force $destDir | Out-Null
        Get-ChildItem $src -File | ForEach-Object {
            $dest = Join-Path $destDir $_.Name
            if (-not (Test-Path $dest)) { Copy-Item $_.FullName $dest; $added++ }
        }
    }
}

Remove-Item -Recurse -Force $tmp
$days = (Get-ChildItem (Join-Path $PSScriptRoot "history\daily") -Filter *.json -ErrorAction SilentlyContinue).Count
Write-Host ("Done. Added {0} new file(s). You now have {1} day(s) of daily history." -f $added, $days)
