# jhs-laser-webui (JHS 雷射打標設備 Web 監控與二次開發系統)

由 **JHS** 二次開發、基於 Go embed 單一執行檔架構的雷射打標控制卡 (USBLMCV4 / EzCad2) 跨平台 Web 監控與控制介面。

## 功能特色
- **JHS 客製化二次開發架構**：在 Windows (GOARCH=386) 下直接透過 LMCMIO.dll 底層接口讀取控制卡狀態、韌體版本、序號、標刻緩衝區、振鏡 XY 座標與 16 路通用輸入 IO (In0~In15)。
- **跨平台支援**：內建硬體抽象層 (LaserController)，支援 Windows 32-bit 實體硬體控制與 Linux 64-bit 模擬測試模式。
- **單一執行檔部署**：前端網頁內嵌於執行檔中，啟動後自動開啟桌面 App 模式視窗。