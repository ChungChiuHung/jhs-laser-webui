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
galvoZ         uint16
axis0Steps     int32
previewing     atomic.Bool
stopFlag       atomic.Bool
previewEndUnix atomic.Int64
lastX          atomic.Uint32
lastY          atomic.Uint32
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
galvoZ:        32768,
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
status.GalvoZ = d.galvoZ
d.lastX.Store(uint32(res.v1))
d.lastY.Store(uint32(res.v2))

// 讀取擴展軸 0 步數座標 (GetAxisPos: 0x002a)，若板卡回傳非 0 則同步更新
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x002a, 0, 0, 0, 0, 0)
hwSteps := int32(uint32(res.v1) | (uint32(res.v2) << 16))
if hwSteps != 0 {
d.axis0Steps = hwSteps
}
status.Axis0Steps = d.axis0Steps
// 依據 markcfg7: AXISSTEPPERRO0=200, AXISDISTPERRO0=5.0mm -> 40 steps/mm
status.Axis0MM = float64(d.axis0Steps) / 40.0

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

// MoveAxisRelMM 控制 CON4 擴展軸 (Z軸升降馬達或旋轉軸) 相對移動指定毫米數
// 依據 markcfg7: 每轉 200 步 / 導程 5mm = 40 步/mm
func (d *WindowsJczDriver) MoveAxisRelMM(axis int, deltaMM float64, speed int) error {
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

if speed < 100 {
speed = 1000
}
if speed > 5000 {
speed = 5000
}

deltaSteps := int32(math.Round(deltaMM * 40.0))
d.axis0Steps += deltaSteps
uPos := uint32(d.axis0Steps)
low16 := uint16(uPos & 0xFFFF)
high16 := uint16((uPos >> 16) & 0xFFFF)

var res tagResult
cardID := uintptr(0)
// 1. 設定擴展軸運動速度參數 SetAxisMotionParam (0x0026): MinSpd=100, MaxSpd=speed
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0026, uintptr(100), uintptr(speed), uintptr(axis), 0, 0)
// 2. 下達擴展軸移動指令 MoveAxisTo (0x0029): parm1=low16, parm2=high16, parm3=axis
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0029, uintptr(low16), uintptr(high16), uintptr(axis), 0, 0)
return nil
}

// AxisGoOrigin 命令 CON4 擴展軸執行自動回原點 (0x0028)
func (d *WindowsJczDriver) AxisGoOrigin(axis int) error {
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
cardID := uintptr(0)
// SetAxisOriginParam (0x0027) & AxisGoOrigin (0x0028)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0027, uintptr(axis), uintptr(100), 0, 0, 0)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0028, uintptr(axis), 0, 0, 0, 0)
d.axis0Steps = 0
return nil
}

// SetGalvoZ 控制 CON1 3D 動態聚焦振鏡 Z 軸 DAC 數值 (ENABLEZ 0x003a + SETZDATA 0x003b)
func (d *WindowsJczDriver) SetGalvoZ(zDac uint16) error {
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

d.galvoZ = zDac
var res tagResult
cardID := uintptr(0)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x003a, 1, 0, 0, 0, 0)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x003b, uintptr(zDac), 0, 0, 0, 0)
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

func (d *WindowsJczDriver) StopPreview() error {
d.stopFlag.Store(true)
return nil
}

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

func (d *WindowsJczDriver) RedLightPreview(sizeMM float64, smoothSteps int) error {
return d.StartContinuousPreview(sizeMM, smoothSteps, 3)
}