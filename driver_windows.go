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
"sync/atomic"
"syscall"
"time"
"unsafe"
)

type tagResult struct {
v0, v1, v2, v3, v4, v5, v6, v7 uint16
}

type WindowsJczDriver struct {
mu            sync.Mutex
ezcadDir      string
outPort       uint16
previewing    atomic.Bool
stopFlag      atomic.Bool
previewEndUnix atomic.Int64
lastX         atomic.Uint32
lastY         atomic.Uint32
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

d := &WindowsJczDriver{
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
d.lastX.Store(32768)
d.lastY.Store(32768)
return d
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
status := BoardStatus{
Platform:      fmt.Sprintf("Windows (%s)", runtime.GOARCH),
PreviewActive: d.previewing.Load(),
}
if status.PreviewActive {
rem := int(d.previewEndUnix.Load() - time.Now().Unix())
if rem < 0 {
rem = 0
}
status.PreviewRemainSec = rem
}
d.readMarkCfg7(&status)

d.mu.Lock()
defer d.mu.Unlock()

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
d.lastX.Store(uint32(res.v1))
d.lastY.Store(uint32(res.v2))

d.procReadPort.Call(uintptr(unsafe.Pointer(&res)), cardID)
ioVal := res.v1
status.InputPortHex = fmt.Sprintf("0x%04X", ioVal)
status.InputPortBin = fmt.Sprintf("%016b", ioVal)

shownOut := d.outPort
if status.PreviewActive {
shownOut |= (1 << 4) | (1 << 8)
}
status.OutputPortHex = fmt.Sprintf("0x%04X", shownOut)
status.OutputPortBin = fmt.Sprintf("%016b", shownOut)

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

func (d *WindowsJczDriver) ultraSilentGlide(fromX, fromY, toX, toY uint16) {
var res tagResult
fx, fy := float64(fromX), float64(fromY)
dx := float64(toX) - fx
dy := float64(toY) - fy
dist := math.Hypot(dx, dy)
steps := int(dist / 2.0)
if steps < 40 {
steps = 40
}

var lx, ly uint16 = fromX, fromY
for s := 1; s <= steps; s++ {
t := float64(s) / float64(steps)
ease := (1.0 - math.Cos(t*math.Pi)) / 2.0
nx := uint16(fx + dx*ease)
ny := uint16(fy + dy*ease)
if nx != lx || ny != ly {
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(nx), uintptr(ny), 0, 0, 0)
lx, ly = nx, ny
}
}
d.lastX.Store(uint32(toX))
d.lastY.Store(uint32(toY))
}

func (d *WindowsJczDriver) GotoXY(x uint16, y uint16) error {
d.stopFlag.Store(true)
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
d.ultraSilentGlide(curX, curY, x, y)
return nil
}

// StopPreview 立即通知背景持續預覽迴圈停止
func (d *WindowsJczDriver) StopPreview() error {
d.stopFlag.Store(true)
return nil
}

// StartContinuousPreview 啟動帶有自動逾時保護 (預設 30 秒) 的背景 0dB 無聲循環預覽
func (d *WindowsJczDriver) StartContinuousPreview(sizeMM float64, smoothSteps int, timeoutSec int) error {
if d.previewing.Load() {
// 若已在預覽中，重新延長倒數計時
if timeoutSec <= 0 || timeoutSec > 120 {
timeoutSec = 30
}
d.previewEndUnix.Store(time.Now().Add(time.Duration(timeoutSec) * time.Second).Unix())
return nil
}

if sizeMM < 2 {
sizeMM = 20
}
if sizeMM > 90 {
sizeMM = 90
}
if timeoutSec <= 0 || timeoutSec > 120 {
timeoutSec = 30
}

halfSpan := (sizeMM / 2.0) * (65535.0 / 110.0)
pointsPerLoop := smoothSteps * 4
if pointsPerLoop < 1600 {
pointsPerLoop = 3200
}

d.stopFlag.Store(false)
d.previewing.Store(true)
deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
d.previewEndUnix.Store(deadline.Unix())

go func() {
defer d.previewing.Store(false)

c := 32768.0
firstX := uint16(c + halfSpan)
firstY := uint16(c)

// 1. 取得鎖：開啟紅光 (Out4 + Out8) 並無聲滑行至預覽框起點
d.mu.Lock()
if err := d.prepareEnv(); err == nil {
ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
d.procOpen.Call(0)
}
if h, _, _ := d.procGetHandle.Call(0); h != 0 {
var res tagResult
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000c, 0, 0, 0, 0, 0)
curX, curY := res.v1, res.v2
if curX == 0 && curY == 0 {
curX, curY = 32768, 32768
}
d.ultraSilentGlide(curX, curY, firstX, firstY)
previewPort := d.outPort | (1 << 4) | (1 << 8)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(previewPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(previewPort), uintptr(0xFFFF), 0, 0, 0)
d.procClose.Call()
}
}
d.mu.Unlock()

var lastX, lastY uint16 = firstX, firstY
step := 0

// 2. 持續循環掃描，每批次執行 160 個微步後短暫讓出鎖，確保前端每秒狀態監控與手動停止零延遲
for !d.stopFlag.Load() && time.Now().Unix() < d.previewEndUnix.Load() {
d.mu.Lock()
if err := d.prepareEnv(); err == nil {
ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
d.procOpen.Call(0)
}
if h, _, _ := d.procGetHandle.Call(0); h != 0 {
var res tagResult
previewPort := d.outPort | (1 << 4) | (1 << 8)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(previewPort), uintptr(0xFFFF))

for batch := 0; batch < 160 && !d.stopFlag.Load(); batch++ {
step++
theta := (float64(step) / float64(pointsPerLoop)) * 2.0 * math.Pi
cosT := math.Cos(theta)
sinT := math.Sin(theta)
signX := 1.0
if cosT < 0 {
signX = -1.0
}
signY := 1.0
if sinT < 0 {
signY = -1.0
}
xNorm := signX * (1.0 - math.Pow(1.0-math.Abs(cosT), 6.0))
yNorm := signY * (1.0 - math.Pow(1.0-math.Abs(sinT), 6.0))

nx := uint16(c + halfSpan*xNorm)
ny := uint16(c + halfSpan*yNorm)
if nx != lastX || ny != lastY {
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(nx), uintptr(ny), 0, 0, 0)
lastX, lastY = nx, ny
}
}
d.lastX.Store(uint32(lastX))
d.lastY.Store(uint32(lastY))
d.procClose.Call()
}
}
d.mu.Unlock()
runtime.Gosched()
}

// 3. 結束預覽 (手動停止或 30 秒到期)：關閉紅光輸出並無聲滑回中心點 (32768, 32768)
d.mu.Lock()
if err := d.prepareEnv(); err == nil {
ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
d.procOpen.Call(0)
}
if h, _, _ := d.procGetHandle.Call(0); h != 0 {
var res tagResult
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(d.outPort), uintptr(0xFFFF), 0, 0, 0)
d.ultraSilentGlide(lastX, lastY, 32768, 32768)
d.procClose.Call()
}
}
d.mu.Unlock()
}()

return nil
}

// RedLightPreview 單次 2 圈無聲預覽 (約 3 秒自動完成)
func (d *WindowsJczDriver) RedLightPreview(sizeMM float64, smoothSteps int) error {
return d.StartContinuousPreview(sizeMM, smoothSteps, 3)
}