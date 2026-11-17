//go:build windows

package main

import (
"bufio"
"fmt"
"math"
"os"
"path/filepath"
"runtime"
"strings"
"sync"
"syscall"
"unsafe"
)

type tagResult struct {
v0, v1, v2, v3, v4, v5, v6, v7 uint16
}

type WindowsJczDriver struct {
mu            sync.Mutex
ezcadDir      string
outPort       uint16
dll           *syscall.LazyDLL
procOpenNew   *syscall.LazyProc
procOpen      *syscall.LazyProc
procGetHandle *syscall.LazyProc
procGetState  *syscall.LazyProc
procGetVer    *syscall.LazyProc
procGetSN     *syscall.LazyProc
procReadPort  *syscall.LazyProc
procWritePort *syscall.LazyProc
procCmd       *syscall.LazyProc
procChipID    *syscall.LazyProc
procClose     *syscall.LazyProc
}

func NewLaserController(ezcadDir string) LaserController {
dllPath := filepath.Join(ezcadDir, "LMCMIO.dll")
dll := syscall.NewLazyDLL(dllPath)

return &WindowsJczDriver{
ezcadDir:      ezcadDir,
dll:           dll,
procOpenNew:   dll.NewProc("?MIO_OpenNewDev@@YAHXZ"),
procOpen:      dll.NewProc("?MIO_Open@@YAHH@Z"),
procGetHandle: dll.NewProc("?MIO_GetDevHandle@@YAPAXH@Z"),
procGetState:  dll.NewProc("?MIO_GetState@@YA?AUtagResult@@E@Z"),
procGetVer:    dll.NewProc("?MIO_GetVersion@@YA?AUtagResult@@EG@Z"),
procGetSN:     dll.NewProc("?MIO_GetSerialNo@@YA?AUtagResult@@E@Z"),
procReadPort:  dll.NewProc("?MIO_ReadPort@@YA?AUtagResult@@E@Z"),
procWritePort: dll.NewProc("?MIO_WritePort@@YA?AUtagResult@@EGG@Z"),
procCmd:       dll.NewProc("?MIO_Cmd@@YA?AUtagResult@@EGGGGGG@Z"),
procChipID:    dll.NewProc("?MIO_GetChipIDState@@YAHEAAG@Z"),
procClose:     dll.NewProc("?MIO_Close@@YAXXZ"),
}
}

func (d *WindowsJczDriver) readMarkCfg7(status *BoardStatus) {
cfgPath := filepath.Join(d.ezcadDir, "plug", "markcfg7")
f, err := os.Open(cfgPath)
if err != nil {
status.FieldSizeMM = "110 x 110 mm"
return
}
defer f.Close()

scanner := bufio.NewScanner(f)
for scanner.Scan() {
line := strings.TrimSpace(scanner.Text())
if strings.HasPrefix(line, "TOTALPARTNUM=") {
status.TotalParts = strings.TrimPrefix(line, "TOTALPARTNUM=")
} else if strings.HasPrefix(line, "MARKTOTALTIME=") {
status.TotalTimeSec = strings.TrimPrefix(line, "MARKTOTALTIME=")
}
}
status.FieldSizeMM = "110 x 110 mm (Raycus Fiber 20-80kHz)"
}

func (d *WindowsJczDriver) prepareEnv() error {
if runtime.GOARCH != "386" {
return fmt.Errorf("LMCMIO.dll requires GOARCH=386")
}
if err := os.Chdir(d.ezcadDir); err != nil {
return err
}
return d.dll.Load()
}

func (d *WindowsJczDriver) GetStatus() BoardStatus {
d.mu.Lock()
defer d.mu.Unlock()

status := BoardStatus{
Platform: fmt.Sprintf("Windows (%s)", runtime.GOARCH),
}
d.readMarkCfg7(&status)

if err := d.prepareEnv(); err != nil {
status.ErrorMsg = err.Error()
return status
}

ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
ret, _, _ = d.procOpen.Call(0)
}

handle, _, _ := d.procGetHandle.Call(0)
if handle == 0 {
status.ErrorMsg = "無法取得控制卡 Handle (請確認 USB 連線正常且 EzCad2.exe 已關閉)"
return status
}
defer d.procClose.Call()

status.Connected = true
status.HandleHex = fmt.Sprintf("0x%X", handle)

var res tagResult
cardID := uintptr(0)

d.procGetState.Call(uintptr(unsafe.Pointer(&res)), cardID)
status.StateHex = fmt.Sprintf("0x%04X / 0x%04X / 0x%04X", res.v0, res.v1, res.v2)

d.procGetVer.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)
status.FirmwareVer = fmt.Sprintf("0x%04X (Sub: 0x%04X)", res.v1, res.v2)

d.procGetSN.Call(uintptr(unsafe.Pointer(&res)), cardID)
status.SerialNo = fmt.Sprintf("%d-%d", res.v1, res.v2)

var chipState uint16
chipRet, _, _ := d.procChipID.Call(cardID, uintptr(unsafe.Pointer(&chipState)))
status.ChipIDState = fmt.Sprintf("Ret=%d | State=0x%04X", int32(chipRet), chipState)

d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x000a, 0, 0, 0, 0, 0)
status.ListReady = (res.v0 == 1)
status.ListStateHex = fmt.Sprintf("0x%04X", res.v0)

d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x000c, 0, 0, 0, 0, 0)
status.GalvoX = res.v1
status.GalvoY = res.v2

d.procReadPort.Call(uintptr(unsafe.Pointer(&res)), cardID)
ioVal := res.v1
status.InputPortHex = fmt.Sprintf("0x%04X", ioVal)
status.InputPortBin = fmt.Sprintf("%016b", ioVal)
status.OutputPortHex = fmt.Sprintf("0x%04X", d.outPort)
status.OutputPortBin = fmt.Sprintf("%016b", d.outPort)

in0 := (ioVal & (1 << 0)) != 0
in2 := (ioVal & (1 << 2)) != 0
status.HeadUnlocked = (in0 && in2)
status.ButtonPressed = (ioVal & (1 << 15)) == 0

return status
}

func (d *WindowsJczDriver) WriteOutputPort(mask uint16, val uint16) error {
d.mu.Lock()
defer d.mu.Unlock()

if err := d.prepareEnv(); err != nil {
return err
}
ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
d.procOpen.Call(0)
}
handle, _, _ := d.procGetHandle.Call(0)
if handle == 0 {
return fmt.Errorf("無法開啟控制卡")
}
defer d.procClose.Call()

d.outPort = (d.outPort & ^mask) | (val & mask)

var res tagResult
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(d.outPort), uintptr(0xFFFF), 0, 0, 0)
return nil
}

// sCurveMove 採用餘弦 S-Curve 柔性加減速曲線，且不使用 Windows time.Sleep (避免 15.6ms 排程器抖動)，讓轉角完全靜音無頓挫
func (d *WindowsJczDriver) sCurveMove(fromX, fromY, toX, toY uint16, steps int) {
if steps < 10 {
steps = 10
}
var res tagResult
fx, fy := float64(fromX), float64(fromY)
dx := float64(toX) - fx
dy := float64(toY) - fy

for s := 1; s <= steps; s++ {
t := float64(s) / float64(steps)
// 餘弦 S-Curve 加減速：起點與轉角處速度平滑趨近於 0，消除急停撞擊聲
ease := (1.0 - math.Cos(t*math.Pi)) / 2.0
nx := uint16(fx + dx*ease)
ny := uint16(fy + dy*ease)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(nx), uintptr(ny), 0, 0, 0)
}
}

func (d *WindowsJczDriver) GotoXY(x uint16, y uint16) error {
d.mu.Lock()
defer d.mu.Unlock()

if err := d.prepareEnv(); err != nil {
return err
}
ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
d.procOpen.Call(0)
}
handle, _, _ := d.procGetHandle.Call(0)
if handle == 0 {
return fmt.Errorf("無法開啟控制卡")
}
defer d.procClose.Call()

var res tagResult
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000c, 0, 0, 0, 0, 0)
curX, curY := res.v1, res.v2
d.sCurveMove(curX, curY, x, y, 200)
return nil
}

// RedLightPreview 採用 S-Curve 餘弦加減速連續微步插補，徹底消除轉角頓挫與 Windows 計時器抖動噪音
func (d *WindowsJczDriver) RedLightPreview(sizeMM float64, smoothSteps int) error {
d.mu.Lock()
defer d.mu.Unlock()

if err := d.prepareEnv(); err != nil {
return err
}
ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
d.procOpen.Call(0)
}
handle, _, _ := d.procGetHandle.Call(0)
if handle == 0 {
return fmt.Errorf("無法開啟控制卡")
}
defer d.procClose.Call()

if sizeMM < 2 {
sizeMM = 20
}
if sizeMM > 90 {
sizeMM = 90
}
if smoothSteps < 50 || smoothSteps > 1200 {
smoothSteps = 300 // 預設每邊 300 步 S-Curve 超靜音插補
}
halfSpan := uint16((sizeMM / 2.0) * (65535.0 / 110.0))

var res tagResult
// 讀取當前振鏡真實位置作為平滑起點
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000c, 0, 0, 0, 0, 0)
startX, startY := res.v1, res.v2
if startX == 0 && startY == 0 {
startX, startY = 32768, 32768
}

c := uint16(32768)
corners := [][2]uint16{
{c - halfSpan, c - halfSpan},
{c + halfSpan, c - halfSpan},
{c + halfSpan, c + halfSpan},
{c - halfSpan, c + halfSpan},
{c - halfSpan, c - halfSpan},
}

// 1. 先在無光狀態下，以 S-Curve 靜音滑行至第一個轉角起點
d.sCurveMove(startX, startY, corners[0][0], corners[0][1], smoothSteps/2)

// 2. 開啟 Out4 (標刻紅光輸出) 與 Out8 (RedPt)
previewPort := d.outPort | (1 << 4) | (1 << 8)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(previewPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(previewPort), uintptr(0xFFFF), 0, 0, 0)

// 3. 以 S-Curve 餘弦加減速連續靜音掃描 3 圈正方形 (每個 90 度轉角皆自動柔順減速再加速)
for loop := 0; loop < 3; loop++ {
for i := 0; i < 4; i++ {
d.sCurveMove(corners[i][0], corners[i][1], corners[i+1][0], corners[i+1][1], smoothSteps)
}
}

// 4. 關閉預覽輸出並以 S-Curve 靜音滑行回中心點 (32768, 32768)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(d.outPort), uintptr(0xFFFF), 0, 0, 0)
d.sCurveMove(corners[4][0], corners[4][1], c, c, smoothSteps/2)
return nil
}