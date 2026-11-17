//go:build windows

package main

import (
"bufio"
"fmt"
"math"
"os"
"path/filepath"
"runtime"
"strconv"
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

// GalvoCalib 儲存從 plug\markcfg7 讀取的出廠光學幾何校正參數
type GalvoCalib struct {
FieldSizeMM   float64
GalvoXSwap    float64
Aspect0       float64
Aspect1       float64
Distor0       float64
Distor1       float64
HorVer0       float64
HorVer1       float64
TrapeDistor0  float64
TrapeDistor1  float64
Negate0       float64
Negate1       float64
RedOffsetX    float64
RedOffsetY    float64
RedScaleX     float64
RedScaleY     float64
}

type WindowsJczDriver struct {
mu              sync.Mutex
ezcadDir        string
outPort         uint16
calib           GalvoCalib
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
ezcadDir: ezcadDir,
outPort:  0x0010,
calib: GalvoCalib{
FieldSizeMM:  110.0,
GalvoXSwap:   1,
Aspect0:      83.684,
Aspect1:      91.371,
Distor0:      0.975,
Distor1:      1.050,
HorVer0:      1.000,
HorVer1:      1.008,
TrapeDistor0: 1.003,
TrapeDistor1: 1.002,
RedOffsetX:   1.0,
RedOffsetY:   1.3,
RedScaleX:    1.02,
RedScaleY:    1.02,
},
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

// mmToGalvoDAC 依照原廠封包驗證之比例 (X 對應 Aspect0=83.684, Y 對應 Aspect1=91.371，比值 0.91587) 進行精確換算
func (d *WindowsJczDriver) mmToGalvoDAC(xMM, yMM float64, isRedLight bool) (uint16, uint16) {
if isRedLight {
xMM = xMM*d.calib.RedScaleX + d.calib.RedOffsetX
yMM = yMM*d.calib.RedScaleY + d.calib.RedOffsetY
}

halfField := d.calib.FieldSizeMM / 2.0
if halfField <= 0 {
halfField = 55.0
}

nx := xMM / halfField
ny := yMM / halfField

// 1. 平行四邊形垂直度校正 (GALVOHORVER0 / GALVOHORVER1)
nx = nx + (d.calib.HorVer0-1.0)*ny
ny = ny + (d.calib.HorVer1-1.0)*nx

// 2. 梯形畸變校正 (GALVOTRAPEDISTOR0 / GALVOTRAPEDISTOR1)
nx = nx * (1.0 + (d.calib.TrapeDistor0-1.0)*ny)
ny = ny * (1.0 + (d.calib.TrapeDistor1-1.0)*nx)

// 3. 枕狀/桶狀畸變校正 (GALVODISTOR0 / GALVODISTOR1)
r2 := nx*nx + ny*ny
nx = nx * (1.0 + (d.calib.Distor0-1.0)*r2)
ny = ny * (1.0 + (d.calib.Distor1-1.0)*r2)

// 4. 關鍵修正：X 軸固定套用 Aspect0 (83.684)，Y 軸固定套用 Aspect1 (91.371)，使 ΔX/ΔY = 0.91587 完全吻合原廠封包
scaleX := d.calib.Aspect0 / 100.0
scaleY := d.calib.Aspect1 / 100.0

if int(math.Round(d.calib.Negate0)) == 1 {
nx = -nx
}
if int(math.Round(d.calib.Negate1)) == 1 {
ny = -ny
}

dacX := 32768.0 + nx*32767.0*scaleX
dacY := 32768.0 + ny*32767.0*scaleY

if dacX < 0 {
dacX = 0
} else if dacX > 65535 {
dacX = 65535
}
if dacY < 0 {
dacY = 0
} else if dacY > 65535 {
dacY = 65535
}
return uint16(math.Round(dacX)), uint16(math.Round(dacY))
}
func (d *WindowsJczDriver) readMarkCfg7(status *BoardStatus) {
cfgPath := filepath.Join(d.ezcadDir, "plug", "markcfg7")
f, err := os.Open(cfgPath)
if err != nil {
status.FieldSizeMM = "110 x 110 mm"
return
}
defer f.Close()

parseVal := func(line, prefix string, target *float64) {
if strings.HasPrefix(line, prefix) {
if v, err := strconv.ParseFloat(strings.TrimPrefix(line, prefix), 64); err == nil {
*target = v
}
}
}

scanner := bufio.NewScanner(f)
for scanner.Scan() {
line := strings.TrimSpace(scanner.Text())
if strings.HasPrefix(line, "TOTALPARTNUM=") {
status.TotalParts = strings.TrimPrefix(line, "TOTALPARTNUM=")
} else if strings.HasPrefix(line, "MARKTOTALTIME=") {
status.TotalTimeSec = strings.TrimPrefix(line, "MARKTOTALTIME=")
}
parseVal(line, "FIELDSIZE=", &d.calib.FieldSizeMM); parseVal(line, "GALVOX=", &d.calib.GalvoXSwap); parseVal(line, "GALVONEGATE0=", &d.calib.Negate0); parseVal(line, "GALVONEGATE1=", &d.calib.Negate1)
parseVal(line, "GALVOASPECT0=", &d.calib.Aspect0)
parseVal(line, "GALVOASPECT1=", &d.calib.Aspect1)
parseVal(line, "GALVODISTOR0=", &d.calib.Distor0)
parseVal(line, "GALVODISTOR1=", &d.calib.Distor1)
parseVal(line, "GALVOHORVER0=", &d.calib.HorVer0)
parseVal(line, "GALVOHORVER1=", &d.calib.HorVer1)
parseVal(line, "GALVOTRAPEDISTOR0=", &d.calib.TrapeDistor0)
parseVal(line, "GALVOTRAPEDISTOR1=", &d.calib.TrapeDistor1)
parseVal(line, "REDLIGHTOFFSETX=", &d.calib.RedOffsetX)
parseVal(line, "REDLIGHTOFFSETY=", &d.calib.RedOffsetY)
parseVal(line, "REDLIGHTSCALE=", &d.calib.RedScaleX)
parseVal(line, "REDLIGHTSCALEY=", &d.calib.RedScaleY)
}
status.FieldSizeMM = fmt.Sprintf("110x110mm [Aspect X:%.2f / Y:%.2f | Distor:%.3f/%.3f]",
d.calib.Aspect0, d.calib.Aspect1, d.calib.Distor0, d.calib.Distor1)
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

func buildFirstPreviewBuf(startX, startY uint16, pts [][2]uint16, speedWord uint16) (tagLmcCmd, int, uint16, uint16) {
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
n := len(pts)
idx := 0
for slot := 7; slot < 256; slot++ {
pt := pts[idx]
dist := uint16(math.Hypot(float64(pt[0])-float64(curX), float64(pt[1])-float64(curY)))
if dist == 0 {
dist = 1
}
base := slot * 6
buf.words[base+0] = 0x8001
buf.words[base+1] = pt[0]
buf.words[base+2] = pt[1]
buf.words[base+3] = 0
buf.words[base+4] = dist
buf.words[base+5] = 0
curX, curY = pt[0], pt[1]
idx = (idx + 1) % n
}
return buf, idx, curX, curY
}

func buildContPreviewBuf(startX, startY uint16, pts [][2]uint16, startIdx int) (tagLmcCmd, int, uint16, uint16) {
var buf tagLmcCmd
curX, curY := startX, startY
n := len(pts)
idx := startIdx % n
for slot := 0; slot < 256; slot++ {
pt := pts[idx]
dist := uint16(math.Hypot(float64(pt[0])-float64(curX), float64(pt[1])-float64(curY)))
if dist == 0 {
dist = 1
}
base := slot * 6
buf.words[base+0] = 0x8001
buf.words[base+1] = pt[0]
buf.words[base+2] = pt[1]
buf.words[base+3] = 0
buf.words[base+4] = dist
buf.words[base+5] = 0
curX, curY = pt[0], pt[1]
idx = (idx + 1) % n
}
return buf, idx, curX, curY
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
return d.StartShapePreview("square", sizeMM, speedParam, 3)
}

func (d *WindowsJczDriver) StopPreview() error {
d.stopFlag.Store(true)
return nil
}

func (d *WindowsJczDriver) StartContinuousPreview(sizeMM float64, speedParam int, timeoutSec int) error {
return d.StartShapePreview("square", sizeMM, speedParam, timeoutSec)
}

func (d *WindowsJczDriver) StartShapePreview(shape string, sizeMM float64, speedParam int, timeoutSec int) error {
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
halfMM := sizeMM / 2.0

var pts [][2]uint16
switch shape {
case "circle":
// 64 段圓形外輪廓點位，經 markcfg7 光學校正比例換算
segs := 64
for i := 0; i < segs; i++ {
ang := (float64(i) / float64(segs)) * 2.0 * math.Pi
px, py := d.mmToGalvoDAC(halfMM*math.Cos(ang), halfMM*math.Sin(ang), true)
pts = append(pts, [2]uint16{px, py})
}
case "cross":
// 中心十字準星往返軌跡
lx, ly := d.mmToGalvoDAC(-halfMM, 0, true)
rx, ry := d.mmToGalvoDAC(halfMM, 0, true)
cx, cy := d.mmToGalvoDAC(0, 0, true)
tx, ty := d.mmToGalvoDAC(0, halfMM, true)
bx, by := d.mmToGalvoDAC(0, -halfMM, true)
pts = [][2]uint16{{lx, ly}, {rx, ry}, {cx, cy}, {tx, ty}, {bx, by}, {cx, cy}}
default: // "square"
x0, y0 := d.mmToGalvoDAC(-halfMM, halfMM, true)
x1, y1 := d.mmToGalvoDAC(halfMM, halfMM, true)
x2, y2 := d.mmToGalvoDAC(halfMM, -halfMM, true)
x3, y3 := d.mmToGalvoDAC(-halfMM, -halfMM, true)
pts = [][2]uint16{{x0, y0}, {x1, y1}, {x2, y2}, {x3, y3}}
}

firstBuf, nextIdx, lastX, lastY := buildFirstPreviewBuf(0x8001, 0x8001, pts, jumpSpeedWord)
contBuf, _, _, _ := buildContPreviewBuf(lastX, lastY, pts, nextIdx)

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
curX, curY = pts[0][0], pts[0][1]
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
// ExecuteLaserMark 套用完整 markcfg7 出廠光學校正公式 (GALVOASPECT0=83.684, GALVOASPECT1=91.371, Distor, TrapeDistor, HorVer) 雕刻 100% 真圓
func (d *WindowsJczDriver) ExecuteLaserMark(req MarkRequest) error {
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
if req.ScaleX < 0.7 || req.ScaleX > 1.3 {
req.ScaleX = 1.0
}
if req.ScaleY < 0.7 || req.ScaleY > 1.3 {
req.ScaleY = 1.0
}
if req.PowerPct < 1 {
req.PowerPct = 20
}
if req.PowerPct > 100 {
req.PowerPct = 100
}
if req.SpeedWord < 50 || req.SpeedWord > 5000 {
req.SpeedWord = 348
}
if req.FreqWord < 100 || req.FreqWord > 5000 {
req.FreqWord = 1000
}

powerWord := uint16(math.Round((req.PowerPct / 100.0) * 4095.0))
radiusX := (req.SizeMM / 2.0) * req.ScaleX
radiusY := (req.SizeMM / 2.0) * req.ScaleY

var buf tagLmcCmd
for s := 0; s < 256; s++ {
buf.words[s*6] = 0x8002
}

buf.words[0*6+0] = 0x8051 // ReadyMark
buf.words[1*6+0] = 0x8004 // MO 預熱延時 = 800
buf.words[1*6+1] = 800
buf.words[2*6+0] = 0x801b // 光纖頻率
buf.words[2*6+1] = req.FreqWord
buf.words[3*6+0] = 0x8012 // 雷射功率
buf.words[3*6+1] = powerWord
buf.words[4*6+0] = 0x8006 // 空移速度 = 2090
buf.words[4*6+1] = 2090
buf.words[5*6+0] = 0x800c // 雕刻速度
buf.words[5*6+1] = req.SpeedWord
buf.words[6*6+0] = 0x8007 // 開光延時 = 250
buf.words[6*6+1] = 250
buf.words[7*6+0] = 0x8008 // 關光延時 = 120
buf.words[7*6+1] = 120
buf.words[8*6+0] = 0x800f // 轉角延時 PolygonDelay = 35
buf.words[8*6+1] = 35
buf.words[9*6+0] = 0x800d // 跳轉延時 JumpDelay = 25
buf.words[9*6+1] = 25

slot := 10
curX, curY := 32769.0, 32769.0

addJump := func(tx, ty uint16) {
dist := uint16(math.Hypot(float64(tx)-curX, float64(ty)-curY))
base := slot * 6
buf.words[base+0] = 0x8001
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
buf.words[base+0] = 0x8005
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
buf.words[base+0] = 0x8004
buf.words[base+1] = 45
slot++
addJump(0x8000, 0x8000)
}

switch req.Shape {
case "circle":
segs := 144
overburnSegs := 3
x0, y0 := d.mmToGalvoDAC(radiusX, 0, false)
addJump(x0, y0)
for i := 1; i <= segs+overburnSegs; i++ {
ang := (float64(i) / float64(segs)) * 2.0 * math.Pi
nx, ny := d.mmToGalvoDAC(radiusX*math.Cos(ang), radiusY*math.Sin(ang), false)
p3 := uint16(3744)
if i == 1 {
p3 = 0
}
addMark(nx, ny, p3)
}
addEndDelay()

case "cross":
lx, ly := d.mmToGalvoDAC(-radiusX, 0, false)
rx, ry := d.mmToGalvoDAC(radiusX, 0, false)
addJump(lx, ly)
addMark(rx, ry, 0)
buf.words[slot*6+0] = 0x8004
buf.words[slot*6+1] = 35
slot++
bx, by := d.mmToGalvoDAC(0, -radiusY, false)
tx, ty := d.mmToGalvoDAC(0, radiusY, false)
addJump(bx, by)
addMark(tx, ty, 0)
addEndDelay()

default: // "square"
p0x, p0y := d.mmToGalvoDAC(-radiusX, radiusY, false)
p1x, p1y := d.mmToGalvoDAC(radiusX, radiusY, false)
p2x, p2y := d.mmToGalvoDAC(radiusX, -radiusY, false)
p3x, p3y := d.mmToGalvoDAC(-radiusX, -radiusY, false)
addJump(p0x, p0y)
addMark(p1x, p1y, 0)
addMark(p2x, p2y, 0)
addMark(p3x, p3y, 0)
addMark(p0x, p0y, 0)
addEndDelay()
}

var res tagResult
cardID := uintptr(0)

d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0000, 0)
d.procResetList.Call(uintptr(unsafe.Pointer(&res)), cardID)
d.procGotoXY.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x8001, 0x8001)
d.procIPGOpenMO.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)

d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&buf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), cardID)

waitMs := int((req.SizeMM*4.5/200.0)*1000.0) + 250
if waitMs < 350 {
waitMs = 350
}
time.Sleep(time.Duration(waitMs) * time.Millisecond)

d.procSetCtrlMode.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0000, 0)
d.procSetStandby.Call(uintptr(unsafe.Pointer(&res)), cardID, 2000, 20, 1)
d.procIPGOpenMO.Call(uintptr(unsafe.Pointer(&res)), cardID, 0)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0041, 3, 0, 0, 0, 0)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0010, 0)
d.outPort = 0x0010
d.lastX.Store(32768)
d.lastY.Store(32768)

return nil
}

// ExecuteQRCodeMark 產生合併閉合輪廓 QRCode 並透過多 Buffer FIFO 分頁串流直接執行 F2 雷射雕刻
func (d *WindowsJczDriver) ExecuteQRCodeMark(req QRVectorRequest) (QRVectorResponse, error) {
vec := GenerateQRVector(req)
if !vec.OK {
return vec, fmt.Errorf(vec.Error)
}

d.stopFlag.Store(true)
d.mu.Lock()
defer d.mu.Unlock()

if err := d.prepareEnv(); err != nil {
return vec, err
}
ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
d.procOpen.Call(0)
}
handle, _, _ := d.procGetHandle.Call(0)
if handle == 0 {
return vec, fmt.Errorf("無法開啟控制卡")
}
defer d.procClose.Call()

scaleX := req.ScaleX
if scaleX < 0.7 || scaleX > 1.3 {
scaleX = 1.0
}
scaleY := req.ScaleY
if scaleY < 0.7 || scaleY > 1.3 {
scaleY = 1.0
}
powerPct := req.PowerPct
if powerPct < 1 {
powerPct = 35
}
if powerPct > 100 {
powerPct = 100
}
speedWord := req.SpeedWord
if speedWord < 50 || speedWord > 5000 {
speedWord = 450
}

powerWord := uint16(math.Round((powerPct / 100.0) * 4095.0))
halfOverall := vec.OverallMM / 2.0

type cmdItem struct {
isMark bool
x, y   uint16
poly   uint16
}
var items []cmdItem

// 1. 加入所有合併閉合輪廓外框
for _, poly := range vec.ContoursMM {
if len(poly) < 3 {
continue
}
x0, y0 := d.mmToGalvoDAC((poly[0][0]-halfOverall)*scaleX, (halfOverall-poly[0][1])*scaleY, false)
items = append(items, cmdItem{isMark: false, x: x0, y: y0})
for i := 1; i < len(poly); i++ {
nx, ny := d.mmToGalvoDAC((poly[i][0]-halfOverall)*scaleX, (halfOverall-poly[i][1])*scaleY, false)
items = append(items, cmdItem{isMark: true, x: nx, y: ny, poly: 0})
}
items = append(items, cmdItem{isMark: true, x: x0, y: y0, poly: 0})
}

// 2. 若設定填充間距 (HatchStepMM > 0)，為暗模組加入雙向弓字形平行填充線
hatchStep := req.HatchStepMM
if hatchStep > 0 && hatchStep < 0.02 {
hatchStep = 0.02
}
if hatchStep > 0 {
mat, _, err := encodePureQR(strings.TrimSpace(req.Text), req.ECC)
if err == nil {
n := mat.size
m := vec.ModuleMM
q := float64(vec.QuietZone) * m
linesPerMod := int(math.Max(1, math.Round(m/hatchStep)))
dirFlip := false
for r := 0; r < n; r++ {
for sub := 0; sub < linesPerMod; sub++ {
yTop := q + (float64(r)+(float64(sub)+0.5)/float64(linesPerMod))*m
yPhys := (halfOverall - yTop) * scaleY
c := 0
for c < n {
if !mat.modules[r][c] {
c++
continue
}
cEnd := c
for cEnd+1 < n && mat.modules[r][cEnd+1] {
cEnd++
}
xL := (q + float64(c)*m - halfOverall) * scaleX
xR := (q + float64(cEnd+1)*m - halfOverall) * scaleX
lx, ly := d.mmToGalvoDAC(xL, yPhys, false)
rx, ry := d.mmToGalvoDAC(xR, yPhys, false)
if !dirFlip {
items = append(items, cmdItem{isMark: false, x: lx, y: ly})
items = append(items, cmdItem{isMark: true, x: rx, y: ry, poly: 0})
} else {
items = append(items, cmdItem{isMark: false, x: rx, y: ry})
items = append(items, cmdItem{isMark: true, x: lx, y: ly, poly: 0})
}
c = cEnd + 1
}
dirFlip = !dirFlip
}
}
}
}

var res tagResult
cardID := uintptr(0)

d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0000, 0)
d.procResetList.Call(uintptr(unsafe.Pointer(&res)), cardID)
d.procGotoXY.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x8001, 0x8001)
d.procIPGOpenMO.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)

curX, curY := 32769.0, 32769.0
idx := 0
total := len(items)
firstPacket := true

for idx < total {
var buf tagLmcCmd
for s := 0; s < 256; s++ {
buf.words[s*6] = 0x8002
}
slot := 0
if firstPacket {
buf.words[0*6+0] = 0x8051
buf.words[1*6+0] = 0x8004
buf.words[1*6+1] = 800
buf.words[2*6+0] = 0x801b
buf.words[2*6+1] = 1000
buf.words[3*6+0] = 0x8012
buf.words[3*6+1] = powerWord
buf.words[4*6+0] = 0x8006
buf.words[4*6+1] = 2400
buf.words[5*6+0] = 0x800c
buf.words[5*6+1] = speedWord
buf.words[6*6+0] = 0x8007
buf.words[6*6+1] = 250
buf.words[7*6+0] = 0x8008
buf.words[7*6+1] = 100
buf.words[8*6+0] = 0x800f
buf.words[8*6+1] = 15
buf.words[9*6+0] = 0x800d
buf.words[9*6+1] = 10
slot = 10
}

maxSlot := 256
if total-idx <= (254 - slot) {
maxSlot = 254
}

for slot < maxSlot && idx < total {
it := items[idx]
dist := uint16(math.Hypot(float64(it.x)-curX, float64(it.y)-curY))
if dist == 0 {
dist = 1
}
base := slot * 6
if it.isMark {
buf.words[base+0] = 0x8005
} else {
buf.words[base+0] = 0x8001
}
buf.words[base+1] = it.x
buf.words[base+2] = it.y
buf.words[base+3] = it.poly
buf.words[base+4] = dist
buf.words[base+5] = 0
curX, curY = float64(it.x), float64(it.y)
slot++
idx++
}

if idx >= total && slot <= 254 {
buf.words[slot*6+0] = 0x8004
buf.words[slot*6+1] = 40
slot++
distHome := uint16(math.Hypot(32768.0-curX, 32768.0-curY))
buf.words[slot*6+0] = 0x8001
buf.words[slot*6+1] = 0x8000
buf.words[slot*6+2] = 0x8000
buf.words[slot*6+4] = distHome
slot++
}

if firstPacket {
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&buf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), cardID)
firstPacket = false
} else {
for {
d.procGetListStat.Call(uintptr(unsafe.Pointer(&res)), cardID)
if res.v0 != 0 {
break
}
time.Sleep(4 * time.Millisecond)
}
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&buf)))
}
}

time.Sleep(220 * time.Millisecond)

d.procSetCtrlMode.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0000, 0)
d.procSetStandby.Call(uintptr(unsafe.Pointer(&res)), cardID, 2000, 20, 1)
d.procIPGOpenMO.Call(uintptr(unsafe.Pointer(&res)), cardID, 0)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0041, 3, 0, 0, 0, 0)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0010, 0)
d.outPort = 0x0010
d.lastX.Store(32768)
d.lastY.Store(32768)

return vec, nil
}
// ExecuteVectorMark 接收前端解析的任意 SVG 多邊形路徑 (含掃描線 Even-Odd 填充 Hatch)，執行多 Buffer FIFO 分頁雷射雕刻
func (d *WindowsJczDriver) ExecuteVectorMark(req VectorMarkRequest) error {
if len(req.Polylines) == 0 {
return fmt.Errorf("沒有可雕刻的 SVG 向量路徑")
}
if req.WidthMM <= 0 || req.HeightMM <= 0 || req.WidthMM > 95 || req.HeightMM > 95 {
return fmt.Errorf("SVG 實體尺寸超出安全工作區範圍 (1~95 mm)")
}

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

scaleX := req.ScaleX
if scaleX < 0.7 || scaleX > 1.3 {
scaleX = 1.0
}
scaleY := req.ScaleY
if scaleY < 0.7 || scaleY > 1.3 {
scaleY = 1.0
}
powerPct := req.PowerPct
if powerPct < 1 {
powerPct = 35
}
if powerPct > 100 {
powerPct = 100
}
speedWord := req.SpeedWord
if speedWord < 50 || speedWord > 5000 {
speedWord = 450
}

powerWord := uint16(math.Round((powerPct / 100.0) * 4095.0))
halfW := req.WidthMM / 2.0
halfH := req.HeightMM / 2.0

type cmdItem struct {
isMark bool
x, y   uint16
poly   uint16
}
var items []cmdItem

// 1. 雕刻所有向量輪廓線段
for _, pl := range req.Polylines {
if len(pl.Points) < 2 {
continue
}
x0, y0 := d.mmToGalvoDAC((pl.Points[0][0]-halfW)*scaleX, (halfH-pl.Points[0][1])*scaleY, false)
items = append(items, cmdItem{isMark: false, x: x0, y: y0})
for i := 1; i < len(pl.Points); i++ {
nx, ny := d.mmToGalvoDAC((pl.Points[i][0]-halfW)*scaleX, (halfH-pl.Points[i][1])*scaleY, false)
items = append(items, cmdItem{isMark: true, x: nx, y: ny, poly: 0})
}
if pl.Closed {
items = append(items, cmdItem{isMark: true, x: x0, y: y0, poly: 0})
}
}

// 2. 若啟用填充 (HatchStepMM > 0)，對閉合多邊形執行 Even-Odd 水平掃描線交點計算並生成弓字形填充線
hatchStep := req.HatchStepMM
if hatchStep > 0 && hatchStep < 0.03 {
hatchStep = 0.03
}
if hatchStep > 0 {
type edge struct{ x1, y1, x2, y2 float64 }
var edges []edge
for _, pl := range req.Polylines {
if !pl.Closed || len(pl.Points) < 3 {
continue
}
for i := 0; i < len(pl.Points); i++ {
p1 := pl.Points[i]
p2 := pl.Points[(i+1)%len(pl.Points)]
if math.Abs(p1[1]-p2[1]) > 1e-6 {
edges = append(edges, edge{p1[0], p1[1], p2[0], p2[1]})
}
}
}

dirFlip := false
for y := hatchStep / 2.0; y < req.HeightMM; y += hatchStep {
var xs []float64
for _, e := range edges {
if (e.y1 <= y && e.y2 > y) || (e.y2 <= y && e.y1 > y) {
t := (y - e.y1) / (e.y2 - e.y1)
xs = append(xs, e.x1+t*(e.x2-e.x1))
}
}
if len(xs) < 2 {
continue
}
// 排序交點並兩兩配對 (Even-Odd 規則自動挖空內部孔洞)
for i := 0; i < len(xs)-1; i++ {
for j := i + 1; j < len(xs); j++ {
if xs[j] < xs[i] {
xs[i], xs[j] = xs[j], xs[i]
}
}
}
yPhys := (halfH - y) * scaleY
if !dirFlip {
for k := 0; k+1 < len(xs); k += 2 {
if xs[k+1]-xs[k] < 0.01 {
continue
}
lx, ly := d.mmToGalvoDAC((xs[k]-halfW)*scaleX, yPhys, false)
rx, ry := d.mmToGalvoDAC((xs[k+1]-halfW)*scaleX, yPhys, false)
items = append(items, cmdItem{isMark: false, x: lx, y: ly})
items = append(items, cmdItem{isMark: true, x: rx, y: ry, poly: 0})
}
} else {
lastPair := (len(xs)/2)*2 - 2
for k := lastPair; k >= 0; k -= 2 {
if xs[k+1]-xs[k] < 0.01 {
continue
}
rx, ry := d.mmToGalvoDAC((xs[k+1]-halfW)*scaleX, yPhys, false)
lx, ly := d.mmToGalvoDAC((xs[k]-halfW)*scaleX, yPhys, false)
items = append(items, cmdItem{isMark: false, x: rx, y: ry})
items = append(items, cmdItem{isMark: true, x: lx, y: ly, poly: 0})
}
}
dirFlip = !dirFlip
}
}

if len(items) == 0 {
return fmt.Errorf("產生的雕刻指令為空")
}

var res tagResult
cardID := uintptr(0)

d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0000, 0)
d.procResetList.Call(uintptr(unsafe.Pointer(&res)), cardID)
d.procGotoXY.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x8001, 0x8001)
d.procIPGOpenMO.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)

curX, curY := 32769.0, 32769.0
idx := 0
total := len(items)
firstPacket := true

for idx < total {
var buf tagLmcCmd
for s := 0; s < 256; s++ {
buf.words[s*6] = 0x8002
}
slot := 0
if firstPacket {
buf.words[0*6+0] = 0x8051
buf.words[1*6+0] = 0x8004
buf.words[1*6+1] = 800
buf.words[2*6+0] = 0x801b
buf.words[2*6+1] = 1000
buf.words[3*6+0] = 0x8012
buf.words[3*6+1] = powerWord
buf.words[4*6+0] = 0x8006
buf.words[4*6+1] = 2400
buf.words[5*6+0] = 0x800c
buf.words[5*6+1] = speedWord
buf.words[6*6+0] = 0x8007
buf.words[6*6+1] = 250
buf.words[7*6+0] = 0x8008
buf.words[7*6+1] = 100
buf.words[8*6+0] = 0x800f
buf.words[8*6+1] = 15
buf.words[9*6+0] = 0x800d
buf.words[9*6+1] = 10
slot = 10
}

maxSlot := 256
if total-idx <= (254 - slot) {
maxSlot = 254
}

for slot < maxSlot && idx < total {
it := items[idx]
dist := uint16(math.Hypot(float64(it.x)-curX, float64(it.y)-curY))
if dist == 0 {
dist = 1
}
base := slot * 6
if it.isMark {
buf.words[base+0] = 0x8005
} else {
buf.words[base+0] = 0x8001
}
buf.words[base+1] = it.x
buf.words[base+2] = it.y
buf.words[base+3] = it.poly
buf.words[base+4] = dist
buf.words[base+5] = 0
curX, curY = float64(it.x), float64(it.y)
slot++
idx++
}

if idx >= total && slot <= 254 {
buf.words[slot*6+0] = 0x8004
buf.words[slot*6+1] = 40
slot++
distHome := uint16(math.Hypot(32768.0-curX, 32768.0-curY))
buf.words[slot*6+0] = 0x8001
buf.words[slot*6+1] = 0x8000
buf.words[slot*6+2] = 0x8000
buf.words[slot*6+4] = distHome
slot++
}

if firstPacket {
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&buf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), cardID)
firstPacket = false
} else {
for {
d.procGetListStat.Call(uintptr(unsafe.Pointer(&res)), cardID)
if res.v0 != 0 {
break
}
time.Sleep(4 * time.Millisecond)
}
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&buf)))
}
}

time.Sleep(240 * time.Millisecond)

d.procSetCtrlMode.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0000, 0)
d.procSetStandby.Call(uintptr(unsafe.Pointer(&res)), cardID, 2000, 20, 1)
d.procIPGOpenMO.Call(uintptr(unsafe.Pointer(&res)), cardID, 0)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0041, 3, 0, 0, 0, 0)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0010, 0)
d.outPort = 0x0010
d.lastX.Store(32768)
d.lastY.Store(32768)

return nil
}