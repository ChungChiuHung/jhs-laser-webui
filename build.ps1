Write-Host "=== 開始編譯 JHS Laser WebUI (Windows x86 & Linux amd64) ===" -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path "dist" | Out-Null
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "386"
go build -ldflags="-s -w" -o "dist\JHS_Laser_WebUI.exe" .
go build -ldflags="-s -w" -o "
G:\CodeHere\Laser_Device\EZCAD_LITE_2.14.16(20210519)
\JHS_Laser_WebUI.exe" .
$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -ldflags="-s -w" -o "dist\jhs-laser-webui-linux-amd64" .
Write-Host "[編譯完成] 已成功輸出至 dist\ 與 EzCad2 目錄！" -ForegroundColor Green