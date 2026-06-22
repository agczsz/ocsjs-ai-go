param(
    [string]$InputFile = "cache.json",
    [string]$OutputFile = ""
)

if (!(Test-Path $InputFile)) {
    Write-Error "文件不存在: $InputFile"
    exit 1
}

try {
    $json = Get-Content $InputFile -Raw -Encoding UTF8 | ConvertFrom-Json
} catch {
    Write-Error "JSON 解析失败: $_"
    exit 1
}

$result = [ordered]@{}

foreach ($item in $json.PSObject.Properties) {
    $obj = $item.Value

    if ($null -ne $obj.answer -and $obj.answer.ToString().Trim() -ne "") {
        $result[$item.Name] = $obj
    }
}

$jsonText = $result | ConvertTo-Json -Depth 100

if ([string]::IsNullOrWhiteSpace($OutputFile)) {
    $jsonText | Set-Content $InputFile -Encoding UTF8
    Write-Host "已清理完成，共保留 $($result.Count) 条记录。"
} else {
    $jsonText | Set-Content $OutputFile -Encoding UTF8
    Write-Host "已清理完成，共保留 $($result.Count) 条记录。"
    Write-Host "输出文件: $OutputFile"
}