package main

import (
"embed"
"encoding/json"
"flag"
"fmt"
"io/fs"
"log"
"net/http"
"os"
"os/exec"
"path/filepath"
"runtime"
"strconv"
"time"
)

//go:embed web/*
var webContent embed.FS

func openBrowser(url string) {
if runtime.GOOS == "windows" {
err := exec.Command("cmd", "/c", "start", "msedge", "--app="+url).Start()
if err != nil {
_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
}
}

type gotoReq struct {
X uint16
Y uint16
}

type portReq struct {
Mask uint16
Val  uint16
}

type redReq struct {
Shape       string
SizeMM      float64
StepDelayMs int
TimeoutSec  int
}

func parseQRReqFromHTTP(r *http.Request) QRVectorRequest {
var req QRVectorRequest
if r.Method == http.MethodPost && r.Body != nil {
_ = json.NewDecoder(r.Body).Decode(&req)
}
q := r.URL.Query()
if v := q.Get("text"); v != "" {
req.Text = v
}
if v := q.Get("size_mm"); v != "" {
if f, err := strconv.ParseFloat(v, 64); err == nil {
req.SizeMM = f
}
}
if v := q.Get("size_basis"); v != "" {
req.SizeBasis = v
}
if v := q.Get("quiet_zone"); v != "" {
if i, err := strconv.Atoi(v); err == nil {
req.QuietZone = i
}
}
if v := q.Get("ecc"); v != "" {
req.ECC = v
}
if v := q.Get("hatch_step_mm"); v != "" {
if f, err := strconv.ParseFloat(v, 64); err == nil {
req.HatchStepMM = f
}
}
if v := q.Get("power_pct"); v != "" {
if f, err := strconv.ParseFloat(v, 64); err == nil {
req.PowerPct = f
}
}
if v := q.Get("speed_word"); v != "" {
if u, err := strconv.ParseUint(v, 10, 16); err == nil {
req.SpeedWord = uint16(u)
}
}
return req
}

func main() {
exePath, _ := os.Executable()
defaultDir := filepath.Dir(exePath)
if _, err := os.Stat(filepath.Join(defaultDir, "LMCMIO.dll")); os.IsNotExist(err) {
defaultDir = `G:\CodeHere\Laser_Device\EZCAD_LITE_2.14.16(20210519)`
}

host := flag.String("host", "127.0.0.1", "監聽位址")
port := flag.Int("port", 8080, "監聽埠號")
ezcadDir := flag.String("dir", defaultDir, "EzCad2 目錄")
dataDir := flag.String("data", "", "佇列持久化目錄 (預設 <dir>/data)")
autoOpen := flag.Bool("open", true, "自動開啟瀏覽器")
flag.Parse()

controller := NewLaserController(*ezcadDir)
mux := http.NewServeMux()

webFS, err := fs.Sub(webContent, "web")
if err != nil {
log.Fatal(err)
}
mux.Handle("/", http.FileServer(http.FS(webFS)))

mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
w.Header().Set("Content-Type", "application/json; charset=utf-8")
json.NewEncoder(w).Encode(controller.GetStatus())
})

mux.HandleFunc("/api/gotoxy", func(w http.ResponseWriter, r *http.Request) {
if r.Method != http.MethodPost {
http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
return
}
var req gotoReq
if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
http.Error(w, err.Error(), http.StatusBadRequest)
return
}
err := controller.GotoXY(req.X, req.Y)
w.Header().Set("Content-Type", "application/json")
if err != nil {
json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
return
}
json.NewEncoder(w).Encode(map[string]any{"ok": true})
})

mux.HandleFunc("/api/writeport", func(w http.ResponseWriter, r *http.Request) {
if r.Method != http.MethodPost {
http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
return
}
var req portReq
if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
http.Error(w, err.Error(), http.StatusBadRequest)
return
}
err := controller.WriteOutputPort(req.Mask, req.Val)
w.Header().Set("Content-Type", "application/json")
if err != nil {
json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
return
}
json.NewEncoder(w).Encode(map[string]any{"ok": true})
})

mux.HandleFunc("/api/redlight", func(w http.ResponseWriter, r *http.Request) {
if r.Method != http.MethodPost {
http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
return
}
var req redReq
_ = json.NewDecoder(r.Body).Decode(&req)
if req.Shape == "" {
req.Shape = "square"
}
err := controller.StartShapePreview(req.Shape, req.SizeMM, req.StepDelayMs, 3)
w.Header().Set("Content-Type", "application/json")
if err != nil {
json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
return
}
json.NewEncoder(w).Encode(map[string]any{"ok": true})
})

mux.HandleFunc("/api/preview/start", func(w http.ResponseWriter, r *http.Request) {
if r.Method != http.MethodPost {
http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
return
}
var req redReq
_ = json.NewDecoder(r.Body).Decode(&req)
if req.Shape == "" {
req.Shape = "square"
}
if req.TimeoutSec <= 0 {
req.TimeoutSec = 30
}
err := controller.StartShapePreview(req.Shape, req.SizeMM, req.StepDelayMs, req.TimeoutSec)
w.Header().Set("Content-Type", "application/json")
if err != nil {
json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
return
}
json.NewEncoder(w).Encode(map[string]any{"ok": true})
})

mux.HandleFunc("/api/preview/stop", func(w http.ResponseWriter, r *http.Request) {
if r.Method != http.MethodPost {
http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
return
}
_ = controller.StopPreview()
w.Header().Set("Content-Type", "application/json")
json.NewEncoder(w).Encode(map[string]any{"ok": true})
})

mux.HandleFunc("/api/mark", func(w http.ResponseWriter, r *http.Request) {
if r.Method != http.MethodPost {
http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
return
}
var req MarkRequest
if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
http.Error(w, err.Error(), http.StatusBadRequest)
return
}
err := controller.ExecuteLaserMark(req)
w.Header().Set("Content-Type", "application/json")
if err != nil {
json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
return
}
json.NewEncoder(w).Encode(map[string]any{"ok": true})
})

// === 外部 API 1：匯入文字/URL 產生雷射用 QRCode 向量資訊 (JSON 含 SVG + DXF + 長寬尺寸) ===
mux.HandleFunc("/api/qrcode/vector", func(w http.ResponseWriter, r *http.Request) {
w.Header().Set("Access-Control-Allow-Origin", "*")
w.Header().Set("Content-Type", "application/json; charset=utf-8")
req := parseQRReqFromHTTP(r)
res := GenerateQRVector(req)
if !res.OK {
w.WriteHeader(http.StatusBadRequest)
}
json.NewEncoder(w).Encode(res)
})

// === 外部 API 2：直接匯出下載雷射專用合併輪廓 SVG 向量檔 ===
mux.HandleFunc("/api/qrcode/svg", func(w http.ResponseWriter, r *http.Request) {
w.Header().Set("Access-Control-Allow-Origin", "*")
req := parseQRReqFromHTTP(r)
res := GenerateQRVector(req)
if !res.OK {
http.Error(w, res.Error, http.StatusBadRequest)
return
}
w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"qrcode-%smm.svg\"", fmtFloat(res.OverallMM)))
_, _ = w.Write([]byte(res.SVG))
})

// === 外部 API 3：直接匯出下載 AutoCAD R12 DXF 雷射向量檔 (相容 EzCad2 / LightBurn) ===
mux.HandleFunc("/api/qrcode/dxf", func(w http.ResponseWriter, r *http.Request) {
w.Header().Set("Access-Control-Allow-Origin", "*")
req := parseQRReqFromHTTP(r)
res := GenerateQRVector(req)
if !res.OK {
http.Error(w, res.Error, http.StatusBadRequest)
return
}
w.Header().Set("Content-Type", "application/dxf")
w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"qrcode-%smm.dxf\"", fmtFloat(res.OverallMM)))
_, _ = w.Write([]byte(res.DXF))
})

// === 外部 API 4：匯入文字/URL 產生 QRCode 並直接執行 F2 雷射打標 ===
mux.HandleFunc("/api/qrcode/mark", func(w http.ResponseWriter, r *http.Request) {
w.Header().Set("Access-Control-Allow-Origin", "*")
w.Header().Set("Content-Type", "application/json; charset=utf-8")
if r.Method != http.MethodPost {
http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
return
}
req := parseQRReqFromHTTP(r)
res, err := controller.ExecuteQRCodeMark(req)
if err != nil {
res.OK = false
res.Error = err.Error()
w.WriteHeader(http.StatusBadRequest)
}
json.NewEncoder(w).Encode(res)
})

if *dataDir == "" {
*dataDir = filepath.Join(*ezcadDir, "data")
}
store, err := NewQueueStore(*dataDir)
if err != nil {
log.Fatalf("初始化佇列失敗 (%s): %v", *dataDir, err)
}
registerQueueRoutes(mux, controller, store, webFS)

addr := fmt.Sprintf("%s:%d", *host, *port)
url := fmt.Sprintf("http://127.0.0.1:%d", *port)
fmt.Printf("=== JHS Laser WebUI 控制中心 (含 QRCode 向量工作室 & 外部 API) ===\n")
fmt.Printf("本機訪問網址 : %s\n", url)
fmt.Printf("QR 向量 API  : %s/api/qrcode/vector?text=HELLO&size_mm=20\n", url)

if *autoOpen {
go func() {
time.Sleep(400 * time.Millisecond)
openBrowser(url)
}()
}
log.Fatal(http.ListenAndServe(addr, mux))
}