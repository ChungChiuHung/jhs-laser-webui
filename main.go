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

func main() {
exePath, _ := os.Executable()
defaultDir := filepath.Dir(exePath)
if _, err := os.Stat(filepath.Join(defaultDir, "LMCMIO.dll")); os.IsNotExist(err) {
defaultDir = `G:\CodeHere\Laser_Device\EZCAD_LITE_2.14.16(20210519)`
}

host := flag.String("host", "127.0.0.1", "監聽位址 (單機版預設 127.0.0.1 免防火牆彈窗)")
port := flag.Int("port", 8080, "Web UI 監聽埠號")
ezcadDir := flag.String("dir", defaultDir, "EzCad2 與 LMCMIO.dll 所在目錄")
autoOpen := flag.Bool("open", true, "啟動時自動開啟瀏覽器")
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
var req struct {
X uint16 `json:"x"`
Y uint16 `json:"y"`
}
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
var req struct {
Mask uint16 `json:"mask"`
Val  uint16 `json:"val"`
}
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

addr := fmt.Sprintf("%s:%d", *host, *port)
url := fmt.Sprintf("http://127.0.0.1:%d", *port)

fmt.Printf("=== JHS Laser WebUI 單機版控制中心 ===\n")
fmt.Printf("本機訪問網址 : %s\n", url)
fmt.Printf("綁定驅動目錄 : %s\n", *ezcadDir)

if *autoOpen {
go func() {
time.Sleep(400 * time.Millisecond)
openBrowser(url)
}()
}

log.Fatal(http.ListenAndServe(addr, mux))
}