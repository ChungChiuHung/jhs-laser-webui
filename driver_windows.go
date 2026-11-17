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
mu             sync.Mutex
ezcadDir       string
outPort        uint16
previewing     atomic.Bool
stopFlag       atomic.Bool
previewEndUnix atomic.Int64
lastX          atomic.Uint32
lastY          atomic.Uint32
cachedStatus   BoardStatus
dll            *syscall.LazyDLL
procOpenNew    *syscall.LazyProc
procOpen       *syscall.LazyProc
procGetHandle  *syscall.LazyProc
procGetState   *syscall.LazyProc
procGetVer     *syscall.LazyProc
procGetSN      *syscall.LazyProc
procReadPort   *syscall.LazyProc
procWritePort  *syscall.LazyProc
procCmd        *syscall.LazyProc
procChipID     *syscall.LazyProc
procClose      *syscall.LazyProc
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
// 關鍵修復：若目前正在執行持續預覽，直接回傳快取狀態與即時座標/倒數秒數，絕不呼叫 MIO_Open/MIO_Close 打斷預覽！
if d.previewing.Load() {
st := d.cachedStatus
st.Connected = true
st.PreviewActive = true
rem := int(d.previewEndUnix.Load() - time.Now().Unix())
if rem < 0 {
rem = 0
}
st.PreviewRemainSec = rem
st.GalvoX = uint16(d.lastX.Load())
st.GalvoY = uint16(d.lastY.Load())
shownOut := d.outPort | (1 << 4) | (1 << 8)
st.OutputPortHex = fmt.Sprintf("0x%04X", shownOut)
st.OutputPortBin = fmt.Sprintf("%016b", shownOut)
return st
}

d.mu.Lock()
defer d.mu.Unlock()

status := BoardStatus{
Platform:      fmt.Sprintf("Windows (%s)", runtime.GOARCH),
PreviewActive: false,
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
d.lastX.Store(uint32(res.v1))
d.lastY.Store(uint32(res.v2))

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

d.cachedStatus = status
return status
}

func (d *WindowsJczDriver) WriteOutputPort(mask uint16, val uint16) error {
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

d.outPort = (d.outPort & ^mask) | (val & mask)

var res tagResult
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(d.outPort), uintptr(0xFFFF), 0, 0, 0)
return nil
}

// sCurveEdge 使用餘弦加減速沿直線邊移動振鏡，轉角柔和不撞擊且速度足以拉出清晰紅光線條
func (d *WindowsJczDriver) sCurveEdge(fromX, fromY, toX, toY uint16, steps int) {
if steps < 15 {
steps = 15
}
var res tagResult
fx, fy := float64(fromX), float64(fromY)
dx := float64(toX) - fx
dy := float64(toY) - fy

for s := 1; s <= steps; s++ {
if d.stopFlag.Load() {
return
}
t := float64(s) / float64(steps)
ease := (1.0 - math.Cos(t*math.Pi)) / 2.0
nx := uint16(fx + dx*ease)
ny := uint16(fy + dy*ease)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(nx), uintptr(ny), 0, 0, 0)
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
if curX == 0 && curY == 0 {
curX, curY = 32768, 32768
}
d.sCurveEdge(curX, curY, x, y, 80)
return nil
}

func (d *WindowsJczDriver) StopPreview() error {
d.stopFlag.Store(true)
return nil
}

// StartContinuousPreview 全程保持單一控制卡 Handle 連續掃描正方形並維持 Out4 點亮，結束時自動歸零
func (d *WindowsJczDriver) StartContinuousPreview(sizeMM float64, smoothSteps int, timeoutSec int) error {
if d.previewing.Load() {
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
if smoothSteps < 20 || smoothSteps > 600 {
smoothSteps = 120 // 預設每邊 120 步 S-Curve：兼顧靜音與紅光正方形視覺暫留清晰度
}
if timeoutSec <= 0 || timeoutSec > 120 {
timeoutSec = 30
}

halfSpan := uint16((sizeMM / 2.0) * (65535.0 / 110.0))

d.stopFlag.Store(false)
d.previewing.Store(true)
deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
d.previewEndUnix.Store(deadline.Unix())

go func() {
d.mu.Lock()
defer func() {
d.previewing.Store(false)
d.mu.Unlock()
}()

if err := d.prepareEnv(); err != nil {
return
}
ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
d.procOpen.Call(0)
}
handle, _, _ := d.procGetHandle.Call(0)
if handle == 0 {
return
}
// 只在整個 30 秒預覽結束或按下停止時，才呼叫一次 procClose！
defer d.procClose.Call()

var res tagResult
c := uint16(32768)
corners := [][2]uint16{
{c - halfSpan, c - halfSpan},
{c + halfSpan, c - halfSpan},
{c + halfSpan, c + halfSpan},
{c - halfSpan, c + halfSpan},
{c - halfSpan, c - halfSpan},
}

// 1. 先平滑移動至正方形起點角
d.sCurveEdge(c, c, corners[0][0], corners[0][1], 60)

// 2. 點亮 Out4 (標刻紅光輸出) 與 Out8
previewPort := d.outPort | (1 << 4) | (1 << 8)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(previewPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(previewPort), uintptr(0xFFFF), 0, 0, 0)

// 3. 持續繞行正方形四邊，直到按下停止或 30 秒倒數結束
for !d.stopFlag.Load() && time.Now().Unix() < d.previewEndUnix.Load() {
for i := 0; i < 4 && !d.stopFlag.Load(); i++ {
d.sCurveEdge(corners[i][0], corners[i][1], corners[i+1][0], corners[i+1][1], smoothSteps)
}
// 每繞完一圈讀取一次輸入 IO，若使用者按了機台實體按鈕 (In15 變 0) 也可隨時更新狀態
d.procReadPort.Call(uintptr(unsafe.Pointer(&res)), 0)
ioVal := res.v1
d.cachedStatus.InputPortHex = fmt.Sprintf("0x%04X", ioVal)
d.cachedStatus.InputPortBin = fmt.Sprintf("%016b", ioVal)
}

// 4. 預覽結束：還原 Out4 輸出狀態並平滑滑回中心點 (32768, 32768)
d.stopFlag.Store(false)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(d.outPort), uintptr(0xFFFF), 0, 0, 0)
curX := uint16(d.lastX.Load())
curY := uint16(d.lastY.Load())
d.sCurveEdge(curX, curY, c, c, 60)
}()

return nil
}