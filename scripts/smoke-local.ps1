[CmdletBinding()]
param(
    [string]$BaseUrl = "http://127.0.0.1:18080",
    [string]$Token = "local-development-token-change-me-000000000000",
    [int]$PartSize = 8 * 1024 * 1024
)

$ErrorActionPreference = "Stop"

function Assert-True {
    param(
        [bool]$Condition,
        [string]$Message
    )

    if (-not $Condition) {
        throw "assertion failed: $Message"
    }
}

function New-RequestHeaders {
    param(
        [hashtable]$Auth,
        [string]$ContentType,
        [switch]$Idempotent
    )

    $headers = @{ Authorization = $Auth.Authorization }
    if ($ContentType) {
        $headers["Content-Type"] = $ContentType
    }
    if ($Idempotent) {
        $headers["Idempotency-Key"] = [guid]::NewGuid().ToString()
    }
    return $headers
}

$auth = @{ Authorization = "Bearer $Token" }
$stamp = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
$folder = $null
$recycled = $false
$purged = $false

try {
    $health = Invoke-RestMethod -Uri "$BaseUrl/healthz" -Method Get
    $ready = Invoke-RestMethod -Uri "$BaseUrl/readyz" -Method Get
    Assert-True ($health.status -eq "ok") "healthz is not ready"
    Assert-True ($ready.status -eq "ready") "readyz is not ready"

    $tenant = (Invoke-RestMethod -Uri "$BaseUrl/api/v1/tenant" -Headers $auth).data
    Assert-True (-not [string]::IsNullOrWhiteSpace($tenant.root_directory_id)) "tenant has no root directory"

    $folderName = "smoke-$stamp"
    $folderBody = @{
        parent_id = $tenant.root_directory_id
        name      = $folderName
    } | ConvertTo-Json -Compress
    $folder = (Invoke-RestMethod -Uri "$BaseUrl/api/v1/directories" -Method Post `
        -Headers (New-RequestHeaders -Auth $auth -ContentType "application/json" -Idempotent) `
        -Body $folderBody).data
    Assert-True ($folder.kind -eq "directory") "created node is not a directory"

    $expectedSize = $PartSize + 257
    $pattern = [Text.Encoding]::UTF8.GetBytes(("asteria-local-smoke-$stamp-" * 4096))
    $bytes = [byte[]]::new($expectedSize)
    for ($offset = 0; $offset -lt $bytes.Length; $offset += $pattern.Length) {
        $count = [Math]::Min($pattern.Length, $bytes.Length - $offset)
        [Array]::Copy($pattern, 0, $bytes, $offset, $count)
    }
    $uploadBody = @{
        parent_id = $folder.id
        name      = "smoke.txt"
        size      = $bytes.Length
        mime_type = "text/plain"
    } | ConvertTo-Json -Compress
    $upload = (Invoke-RestMethod -Uri "$BaseUrl/api/v1/uploads" -Method Post `
        -Headers (New-RequestHeaders -Auth $auth -ContentType "application/json" -Idempotent) `
        -Body $uploadBody).data
    Assert-True ($upload.part_size -eq $PartSize) "upload part size differs from the requested smoke-test part size"

    $completedParts = @()
    $partCount = [Math]::Ceiling($bytes.Length / $PartSize)
    for ($index = 0; $index -lt $partCount; $index++) {
        $partNumber = $index + 1
        $start = $index * $PartSize
        $size = [Math]::Min($PartSize, $bytes.Length - $start)
        $chunk = [byte[]]::new($size)
        [Array]::Copy($bytes, $start, $chunk, 0, $size)

        $signedPart = (Invoke-RestMethod -Uri "$BaseUrl/api/v1/uploads/$($upload.id)/parts/sign" -Method Post `
            -Headers (New-RequestHeaders -Auth $auth -ContentType "application/json") `
            -Body (@{ part_number = $partNumber } | ConvertTo-Json -Compress)).data
        $partHeaders = @{}
        if ($signedPart.required_headers) {
            $signedPart.required_headers.PSObject.Properties | ForEach-Object {
                $partHeaders[$_.Name] = [string]$_.Value
            }
        }
        if (-not $partHeaders.ContainsKey("Content-Type")) {
            $partHeaders["Content-Type"] = "application/octet-stream"
        }

        $objectResponse = Invoke-WebRequest -Uri $signedPart.url -Method Put -Headers $partHeaders -Body $chunk
        $etag = [string]$objectResponse.Headers["ETag"]
        Assert-True (-not [string]::IsNullOrWhiteSpace($etag)) "object storage PUT returned no ETag for part $partNumber"
        $completedParts += @{
            part_number = $signedPart.part_number
            etag        = $etag
            size        = $size
        }
    }

    $completeBody = @{
        parts = $completedParts
    } | ConvertTo-Json -Compress -Depth 4
    $complete = (Invoke-RestMethod -Uri "$BaseUrl/api/v1/uploads/$($upload.id)/complete" -Method Post `
        -Headers (New-RequestHeaders -Auth $auth -ContentType "application/json") `
        -Body $completeBody).data
    Assert-True ($complete.upload.status -eq "committed") "upload was not committed"
    Assert-True ($complete.file.size -eq $bytes.Length) "committed file size differs from the source"

    $download = (Invoke-RestMethod -Uri "$BaseUrl/api/v1/files/$($complete.file.id)/download-authorizations" -Method Post `
        -Headers (New-RequestHeaders -Auth $auth -ContentType "application/json") `
        -Body '{}').data
    $client = [System.Net.WebClient]::new()
    try {
        $downloaded = $client.DownloadData($download.url)
    }
    finally {
        $client.Dispose()
    }
    Assert-True ([Linq.Enumerable]::SequenceEqual([byte[]]$bytes, [byte[]]$downloaded)) "downloaded bytes differ from uploaded bytes"

    $children = (Invoke-RestMethod -Uri "$BaseUrl/api/v1/directories/$($folder.id)/children?limit=100" -Headers $auth).data
    Assert-True (@($children | Where-Object { $_.id -eq $complete.file.id }).Count -eq 1) "committed file is missing from its parent listing"

    $recycleHeaders = @{
        Authorization = $auth.Authorization
        "If-Match"    = '"' + $folder.revision + '"'
    }
    Invoke-WebRequest -Uri "$BaseUrl/api/v1/nodes/$($folder.id)" -Method Delete -Headers $recycleHeaders | Out-Null
    $recycled = $true

    $recycleEntries = (Invoke-RestMethod -Uri "$BaseUrl/api/v1/recycle-bin?limit=100" -Headers $auth).data
    $recycleEntry = $recycleEntries | Where-Object { $_.node.id -eq $folder.id } | Select-Object -First 1
    Assert-True ($null -ne $recycleEntry) "recycled folder is missing from the recycle bin"

    $purgeHeaders = @{
        Authorization = $auth.Authorization
        "If-Match"    = '"' + $recycleEntry.node.revision + '"'
    }
    Invoke-WebRequest -Uri "$BaseUrl/api/v1/recycle-bin/$($folder.id)" -Method Delete -Headers $purgeHeaders | Out-Null
    $purged = $true

    [pscustomobject]@{
        base_url       = $BaseUrl
        tenant         = $tenant.display_name
        folder_created = $true
        upload_status  = $complete.upload.status
        bytes_match    = $true
        listing_match  = $true
        part_count     = $partCount
        recycled       = $recycled
        purged         = $purged
    } | ConvertTo-Json -Compress
}
catch {
    if ($folder -and -not $recycled) {
        Write-Warning "smoke test left folder $($folder.id) in the active namespace: $($_.Exception.Message)"
    }
    throw
}
