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

type tagLmcCmd struct {
words [1536]uint16
}

type WindowsJczDriver struct {
mu              sync.Mutex
ezcadDir        string
outPort         uint16
previewing      atomic.Bool
stopFlag        atomic.Bool
previewEndUnix  atomic.Int64
lastX           atomic.Uint32
lastY           atomic.Uint32
cachedStatus    BoardStatus
dll             *syscall.LazyDLL
procOpenNew     *syscall.LazyProc
procOpen        *syscall.LazyProc
procGetHandle   *syscall.LazyProc
procGetState    *syscall.LazyProc
procGetVer      *syscall.LazyProc
procGetSN       *syscall.LazyProc
procReadPort    *syscall.LazyProc
procWritePort   *syscall.LazyProc
procGotoXY      *syscall.LazyProc
procGetPosXY    *syscall.LazyProc
procResetList   *syscall.LazyProc
procWriteCmdBuf *syscall.LazyProc
procExecuteList *syscall.LazyProc
procStopExecute *syscall.LazyProc
procGetListStat *syscall.LazyProc
procSetCtrlMode *syscall.LazyProc
procSetStandby  *syscall.LazyProc
procIPGOpenMO   *syscall.LazyProc
procCmd         *syscall.LazyProc
procChipID      *syscall.LazyProc
procClose       *syscall.LazyProc
}

func NewLaserController(ezcadDir string) LaserController {
dllPath := filepath.Join(ezcadDir, "LMCMIO.dll")
dll := syscall.NewLazyDLL(dllPath)

d := &WindowsJczDriver{
ezcadDir:        ezcadDir,
outPort:         0x0010, // 原廠待機狀態: 0x0010 (Out4 Active-Low 高電位關閉)
dll:             dll,
procOpenNew:     dll.NewProc("?MIO_OpenNewDev@@YAHXZ"),
procOpen:        dll.NewProc("?MIO_Open@@YAHH@Z"),
procGetHandle:   dll.NewProc("?MIO_GetDevHandle@@YAPAXH@Z"),
procGetState:    dll.NewProc("?MIO_GetState@@YA?AUtagResult@@E@Z"),
procGetVer:      dll.NewProc("?MIO_GetVersion@@YA?AUtagResult@@EG@Z"),
procGetSN:       dll.NewProc("?MIO_GetSerialNo@@YA?AUtagResult@@E@Z"),
procReadPort:    dll.NewProc("?MIO_ReadPort@@YA?AUtagResult@@E@Z"),
procWritePort:   dll.NewProc("?MIO_WritePort@@YA?AUtagResult@@EGG@Z"),
procGotoXY:      dll.NewProc("?MIO_GotoXY@@YA?AUtagResult@@EGG@Z"),
procGetPosXY:    dll.NewProc("?MIO_GetPositionXY@@YA?AUtagResult@@E@Z"),
procResetList:   dll.NewProc("?MIO_ResetList@@YA?AUtagResult@@E@Z"),
procWriteCmdBuf: dll.NewProc("?MIO_WriteCmdBuf@@YA?AUtagResult@@EPAUtagLmcCmd@@@Z"),
procExecuteList: dll.NewProc("?MIO_ExecuteList@@YA?AUtagResult@@E@Z"),
procStopExecute: dll.NewProc("?MIO_StopExecute@@YA?AUtagResult@@E@Z"),
procGetListStat: dll.NewProc("?MIO_GetListStatus@@YA?AUtagResult@@E@Z"),
procSetCtrlMode: dll.NewProc("?MIO_SetControlMode@@YA?AUtagResult@@EG@Z"),
procSetStandby:  dll.NewProc("?MIO_SetStandby@@YA?AUtagResult@@EGGH@Z"),
procIPGOpenMO:   dll.NewProc("?MIO_IPG_OpenMO@@YA?AUtagResult@@EH@Z"),
procCmd:         dll.NewProc("?MIO_Cmd@@YA?AUtagResult@@EGGGGGG@Z"),
procChipID:      dll.NewProc("?MIO_GetChipIDState@@YAHEAAG@Z"),
procClose:       dll.NewProc("?MIO_Close@@YAXXZ"),
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
if d.previewing.Load() {
st := d.cachedStatus
st.Platform = fmt.Sprintf("Windows (%s)", runtime.GOARCH)
st.Connected = true
st.PreviewActive = true
rem := int(d.previewEndUnix.Load() - time.Now().Unix())
if rem < 0 {
rem = 0
}
st.PreviewRemainSec = rem
st.GalvoX = uint16(d.lastX.Load())
st.GalvoY = uint16(d.lastY.Load())
st.OutputPortHex = "0x0110"
st.OutputPortBin = fmt.Sprintf("%016b", 0x0110)
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

d.procGetListStat.Call(uintptr(unsafe.Pointer(&res)), cardID)
status.ListReady = (res.v0 == 1)
status.ListStateHex = fmt.Sprintf("0x%04X", res.v0)

d.procGetPosXY.Call(uintptr(unsafe.Pointer(&res)), cardID)
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
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), 0)
return nil
}

func buildFirstPreviewBuf(startX, startY uint16, corners [4][2]uint16, speedWord uint16) (tagLmcCmd, int, uint16, uint16) {
var buf tagLmcCmd
if speedWord == 0 {
speedWord = 0x082a
}
buf.words[0*6+0] = 0x8051
buf.words[1*6+0] = 0x8006
buf.words[1*6+1] = speedWord
buf.words[2*6+0] = 0x800c
buf.words[2*6+1] = speedWord
buf.words[3*6+0] = 0x8007
buf.words[4*6+0] = 0x8008
buf.words[5*6+0] = 0x800f
buf.words[6*6+0] = 0x800d
buf.words[6*6+1] = 0x0000

curX, curY := startX, startY
cIdx := 0
for slot := 7; slot < 256; slot++ {
pt := corners[cIdx]
dist := uint16(math.Hypot(float64(pt[0])-float64(curX), float64(pt[1])-float64(curY)))
base := slot * 6
buf.words[base+0] = 0x8001
buf.words[base+1] = pt[0]
buf.words[base+2] = pt[1]
buf.words[base+3] = 0
buf.words[base+4] = dist
buf.words[base+5] = 0
curX, curY = pt[0], pt[1]
cIdx = (cIdx + 1) % 4
}
return buf, cIdx, curX, curY
}

func buildContPreviewBuf(startX, startY uint16, corners [4][2]uint16, startCornerIdx int) (tagLmcCmd, int, uint16, uint16) {
var buf tagLmcCmd
curX, curY := startX, startY
cIdx := startCornerIdx
for slot := 0; slot < 256; slot++ {
pt := corners[cIdx]
dist := uint16(math.Hypot(float64(pt[0])-float64(curX), float64(pt[1])-float64(curY)))
base := slot * 6
buf.words[base+0] = 0x8001
buf.words[base+1] = pt[0]
buf.words[base+2] = pt[1]
buf.words[base+3] = 0
buf.words[base+4] = dist
buf.words[base+5] = 0
curX, curY = pt[0], pt[1]
cIdx = (cIdx + 1) % 4
}
return buf, cIdx, curX, curY
}

func buildMoveSingleBuf(fromX, fromY, toX, toY uint16, speedWord uint16) tagLmcCmd {
var buf tagLmcCmd
for slot := 0; slot < 256; slot++ {
buf.words[slot*6] = 0x8002
}
if speedWord == 0 {
speedWord = 0x082a
}
dist := uint16(math.Hypot(float64(toX)-float64(fromX), float64(toY)-float64(fromY)))
buf.words[0*6+0] = 0x8051
buf.words[1*6+0] = 0x8006
buf.words[1*6+1] = speedWord
buf.words[2*6+0] = 0x800d
buf.words[2*6+1] = 25
buf.words[3*6+0] = 0x8001
buf.words[3*6+1] = toX
buf.words[3*6+2] = toY
buf.words[3*6+3] = 0
buf.words[3*6+4] = dist
buf.words[3*6+5] = 0
return buf
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
cardID := uintptr(0)
d.procGetPosXY.Call(uintptr(unsafe.Pointer(&res)), cardID)
curX, curY := res.v1, res.v2
if curX == 0 && curY == 0 {
curX, curY = 32768, 32768
}

d.procStopExecute.Call(uintptr(unsafe.Pointer(&res)), cardID)
d.procResetList.Call(uintptr(unsafe.Pointer(&res)), cardID)
d.procGotoXY.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(curX), uintptr(curY))
buf := buildMoveSingleBuf(curX, curY, x, y, 0x082a)
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&buf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), cardID)
time.Sleep(35 * time.Millisecond)

d.lastX.Store(uint32(x))
d.lastY.Store(uint32(y))
return nil
}

func (d *WindowsJczDriver) RedLightPreview(sizeMM float64, speedParam int) error {
return d.StartContinuousPreview(sizeMM, speedParam, 3)
}

func (d *WindowsJczDriver) StopPreview() error {
d.stopFlag.Store(true)
return nil
}

func (d *WindowsJczDriver) StartContinuousPreview(sizeMM float64, speedParam int, timeoutSec int) error {
if d.previewing.Load() {
if timeoutSec <= 0 || timeoutSec > 120 {
timeoutSec = 30
}
d.previewEndUnix.Store(time.Now().Add(time.Duration(timeoutSec) * time.Second).Unix())
return nil
}

if sizeMM < 2 {
sizeMM = 15
}
if sizeMM > 90 {
sizeMM = 90
}
if timeoutSec <= 0 || timeoutSec > 120 {
timeoutSec = 30
}
jumpSpeedWord := uint16(speedParam)
if jumpSpeedWord < 200 || jumpSpeedWord > 10000 {
jumpSpeedWord = 0x082a
}

halfSpan := uint16((sizeMM / 2.0) * (65535.0 / 110.0))
d.stopFlag.Store(false)
d.previewing.Store(true)
d.previewEndUnix.Store(time.Now().Add(time.Duration(timeoutSec) * time.Second).Unix())

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
defer d.procClose.Call()

var res tagResult
cardID := uintptr(0)
c := uint16(32768)

corners := [4][2]uint16{
{c - halfSpan, c + halfSpan},
{c + halfSpan, c + halfSpan},
{c + halfSpan, c - halfSpan},
{c - halfSpan, c - halfSpan},
}

firstBuf, nextIdx, lastX, lastY := buildFirstPreviewBuf(0x8001, 0x8001, corners, jumpSpeedWord)
contBuf, _, _, _ := buildContPreviewBuf(lastX, lastY, corners, nextIdx)

d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0110, 0)
d.procResetList.Call(uintptr(unsafe.Pointer(&res)), cardID)
d.procGotoXY.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x8001, 0x8001)

d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&firstBuf)))
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&contBuf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), cardID)

for !d.stopFlag.Load() && time.Now().Unix() < d.previewEndUnix.Load() {
d.procGetListStat.Call(uintptr(unsafe.Pointer(&res)), cardID)
if res.v0 != 0 {
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&contBuf)))
} else {
time.Sleep(5 * time.Millisecond)
}
}

d.procStopExecute.Call(uintptr(unsafe.Pointer(&res)), cardID)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0110, 0)
d.procResetList.Call(uintptr(unsafe.Pointer(&res)), cardID)

d.procGetPosXY.Call(uintptr(unsafe.Pointer(&res)), cardID)
curX, curY := res.v1, res.v2
if curX == 0 && curY == 0 {
curX, curY = corners[0][0], corners[0][1]
}
d.procGotoXY.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(curX), uintptr(curY))

homeBuf := buildMoveSingleBuf(curX, curY, 0x8000, 0x8000, 0x082a)
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&homeBuf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), cardID)
time.Sleep(30 * time.Millisecond)

d.procSetCtrlMode.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0110, 0)
d.procSetStandby.Call(uintptr(unsafe.Pointer(&res)), cardID, 2000, 20, 1)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(d.outPort), 0)

d.lastX.Store(32768)
d.lastY.Store(32768)
}()

return nil
}

// ExecuteLaserMark 100% 複製 Frida 攔截到的原廠 F2 實際雷射打標序列！
func (d *WindowsJczDriver) ExecuteLaserMark(req MarkRequest) error {
// 先確保停止任何進行中的紅光預覽
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

if req.SizeMM < 1 {
req.SizeMM = 10
}
if req.SizeMM > 90 {
req.SizeMM = 90
}
if req.PowerPct < 1 {
req.PowerPct = 20
}
if req.PowerPct > 100 {
req.PowerPct = 100
}
if req.SpeedWord < 50 || req.SpeedWord > 5000 {
req.SpeedWord = 348 // 原廠預設 0x015c (348)
}
if req.FreqWord < 100 || req.FreqWord > 5000 {
req.FreqWord = 1000 // 原廠預設 0x03e8 (1000 = 20kHz)
}

// 換算 0x8012 功率參數 (原廠 80% 對應 3275 = 0x0ccb，即 100% = 4095)
powerWord := uint16(math.Round((req.PowerPct / 100.0) * 4095.0))
halfSpan := float64(req.SizeMM/2.0) * (65535.0 / 110.0)
c := 32768.0

// 組裝 256-Slot 清單緩衝區 (未使用的 Slot 預設填 0x8002 EndOfList)
var buf tagLmcCmd
for s := 0; s < 256; s++ {
buf.words[s*6] = 0x8002
}

// === 原廠 F2 標頭 Slot 0 ~ Slot 9 ===
buf.words[0*6+0] = 0x8051 // ReadyMark
buf.words[1*6+0] = 0x8004 // listDelayTime (MO 預熱延時 = 800)
buf.words[1*6+1] = 800
buf.words[2*6+0] = 0x801b // listMarkFreq (光纖頻率 = 1000)
buf.words[2*6+1] = req.FreqWord
buf.words[3*6+0] = 0x8012 // listMarkPowerRatio (雷射功率)
buf.words[3*6+1] = powerWord
buf.words[4*6+0] = 0x8006 // listJumpSpeed (空移速度 = 2787)
buf.words[4*6+1] = 2787
buf.words[5*6+0] = 0x800c // listMarkSpeed (雕刻速度 = 348)
buf.words[5*6+1] = req.SpeedWord
buf.words[6*6+0] = 0x8007 // listLaserOnDelay = 300
buf.words[6*6+1] = 300
buf.words[7*6+0] = 0x8008 // listLaserOffDelay = 100
buf.words[7*6+1] = 100
buf.words[8*6+0] = 0x800f // listPolygonDelay = 10
buf.words[8*6+1] = 10
buf.words[9*6+0] = 0x800d // listJumpDelay = 7
buf.words[9*6+1] = 7

slot := 10
curX, curY := 32769.0, 32769.0

addJump := func(tx, ty uint16) {
dist := uint16(math.Hypot(float64(tx)-curX, float64(ty)-curY))
base := slot * 6
buf.words[base+0] = 0x8001 // listJumpTo
buf.words[base+1] = tx
buf.words[base+2] = ty
buf.words[base+3] = 0
buf.words[base+4] = dist
buf.words[base+5] = 0
curX, curY = float64(tx), float64(ty)
slot++
}

addMark := func(tx, ty, polyParam uint16) {
dist := uint16(math.Hypot(float64(tx)-curX, float64(ty)-curY))
if dist == 0 {
dist = 1
}
base := slot * 6
buf.words[base+0] = 0x8005 // listMarkTo (實際出光雕刻直線)
buf.words[base+1] = tx
buf.words[base+2] = ty
buf.words[base+3] = polyParam
buf.words[base+4] = dist
buf.words[base+5] = 0
curX, curY = float64(tx), float64(ty)
slot++
}

addEndDelay := func() {
base := slot * 6
buf.words[base+0] = 0x8004 // MarkEndDelay = 30
buf.words[base+1] = 30
slot++
// 回中心點 (32768, 32768)
addJump(0x8000, 0x8000)
}

switch req.Shape {
case "circle":
// 36 段多邊形圓形 (與您剛剛在 EzCad2 打標的 36 段圓形完全相同，p3=3744)
segs := 36
x0 := uint16(c + halfSpan)
y0 := uint16(c)
addJump(x0, y0)
for i := 1; i <= segs; i++ {
ang := (float64(i) / float64(segs)) * 2.0 * math.Pi
nx := uint16(c + halfSpan*math.Cos(ang))
ny := uint16(c + halfSpan*math.Sin(ang))
p3 := uint16(3744)
if i == 1 {
p3 = 0
}
addMark(nx, ny, p3)
}
addEndDelay()

case "cross":
// 中心十字準星雕刻
addJump(uint16(c-halfSpan), uint16(c))
addMark(uint16(c+halfSpan), uint16(c), 0)
buf.words[slot*6+0] = 0x8004
buf.words[slot*6+1] = 30
slot++
addJump(uint16(c), uint16(c-halfSpan))
addMark(uint16(c), uint16(c+halfSpan), 0)
addEndDelay()

default: // "square" 正方形雕刻
xMin, xMax := uint16(c-halfSpan), uint16(c+halfSpan)
yMin, yMax := uint16(c-halfSpan), uint16(c+halfSpan)
addJump(xMin, yMax)
addMark(xMax, yMax, 0)
addMark(xMax, yMin, 0)
addMark(xMin, yMin, 0)
addMark(xMin, yMax, 0)
addEndDelay()
}

var res tagResult
cardID := uintptr(0)

// === 1. 原廠 F2 啟動序列 ===
// [MIO_WritePort] arg1=0x0 arg2=0x0 (拉低 Out4 啟動標刻輸出)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0000, 0)
// [MIO_ResetList]
d.procResetList.Call(uintptr(unsafe.Pointer(&res)), cardID)
// [MIO_GotoXY] X=32769(0x8001) Y=32769(0x8001)
d.procGotoXY.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x8001, 0x8001)
// ★ [MIO_IPG_OpenMO] 主振盪器 MO 開關 = 1 (cmd=0x0033 p1=1)
d.procIPGOpenMO.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)

// === 2. 寫入雕刻清單並執行 ===
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&buf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), cardID)

// 等待硬體雕刻清單執行完成 (依尺寸與速度計算物理時間 + 安全緩衝)
waitMs := int((req.SizeMM*4.5/300.0)*1000.0) + 180
if waitMs < 250 {
waitMs = 250
}
time.Sleep(time.Duration(waitMs) * time.Millisecond)

// === 3. 原廠 F2 安全收尾序列 (關閉 MO 主振盪器並將 Out4 拉回 0x0010 待機) ===
d.procSetCtrlMode.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0000, 0)
d.procSetStandby.Call(uintptr(unsafe.Pointer(&res)), cardID, 2000, 20, 1)
// ★ [MIO_IPG_OpenMO] 主振盪器 MO 開關 = 0 (立即切斷雷射源主振盪器！)
d.procIPGOpenMO.Call(uintptr(unsafe.Pointer(&res)), cardID, 0)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0041, 3, 0, 0, 0, 0)
// [MIO_WritePort] arg1=0x10 arg2=0x0 (恢復 Out4 待機狀態)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0010, 0)
d.outPort = 0x0010
d.lastX.Store(32768)
d.lastY.Store(32768)

return nil
}