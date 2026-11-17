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
procCmd         *syscall.LazyProc
procChipID      *syscall.LazyProc
procClose       *syscall.LazyProc
}

func NewLaserController(ezcadDir string) LaserController {
dllPath := filepath.Join(ezcadDir, "LMCMIO.dll")
dll := syscall.NewLazyDLL(dllPath)

d := &WindowsJczDriver{
ezcadDir:        ezcadDir,
outPort:         0x0000,
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
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(d.outPort), 0, 0, 0, 0)
return nil
}

// makeFpgaCmdBuf 組裝原廠 EzCad2 的 tagLmcCmd 硬體插補清單
func makeFpgaCmdBuf(startX, startY uint16, points [][2]uint16, jumpSpeedWord uint16) tagLmcCmd {
var buf tagLmcCmd
for slot := 0; slot < 256; slot++ {
buf.words[slot*6] = 0x8002 // listEndofList
}

buf.words[0] = 0x8051 // ReadyMark
if jumpSpeedWord == 0 {
jumpSpeedWord = 0x082a
}
buf.words[6] = 0x8006 // listJumpSpeed
buf.words[7] = jumpSpeedWord
buf.words[12] = 0x800d // listJumpDelay
buf.words[13] = 0x0008

// 在清單內同步開啟紅光埠 listWritrPort (0x8011): 0x0110 (Out4 + Out8)
buf.words[18] = 0x8011
buf.words[19] = 0x0110

curX, curY := float64(startX), float64(startY)
slot := 4
for _, pt := range points {
if slot >= 252 {
break
}
tx, ty := float64(pt[0]), float64(pt[1])
dist := uint16(math.Hypot(tx-curX, ty-curY))
if dist == 0 {
dist = 1
}
base := slot * 6
buf.words[base+0] = 0x8001 // listJumpTo
buf.words[base+1] = pt[0]
buf.words[base+2] = pt[1]
buf.words[base+3] = 0x0000
buf.words[base+4] = dist
buf.words[base+5] = 0x0000
curX, curY = tx, ty
slot++
}
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
d.procGetPosXY.Call(uintptr(unsafe.Pointer(&res)), 0)
curX, curY := res.v1, res.v2
if curX == 0 && curY == 0 {
curX, curY = 32768, 32768
}

d.procResetList.Call(uintptr(unsafe.Pointer(&res)), 0)
d.procGotoXY.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(curX), uintptr(curY))
buf := makeFpgaCmdBuf(curX, curY, [][2]uint16{{x, y}}, 0x082a)
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(unsafe.Pointer(&buf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), 0)
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

// StartContinuousPreview 修正提前 ResetList 的問題：每批次執行 4 圈正方形並精確等待物理掃描時間，同時點亮 Out4 (0x0010) 與 Out8 (0x0100)
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
// 標準正方形四個頂點：左下 -> 右下 -> 右上 -> 左上
c0 := [2]uint16{c - halfSpan, c - halfSpan}
c1 := [2]uint16{c + halfSpan, c - halfSpan}
c2 := [2]uint16{c + halfSpan, c + halfSpan}
c3 := [2]uint16{c - halfSpan, c + halfSpan}

// 每批組裝 6 圈正方形 (24 個頂點)
loopsPerBatch := 6
var batchPts [][2]uint16
for l := 0; l < loopsPerBatch; l++ {
batchPts = append(batchPts, c1, c2, c3, c0)
}
cmdBuf := makeFpgaCmdBuf(c0[0], c0[1], batchPts, jumpSpeedWord)

// 計算這 6 圈在 FPGA 上的真實物理執行時間 (毫秒)：總周長 = sizeMM * 4 * 6，速度約 2800 mm/s
totalDistMM := sizeMM * 4.0 * float64(loopsPerBatch)
batchDurationMs := int((totalDistMM / 2600.0) * 1000.0) + 18
if batchDurationMs < 60 {
batchDurationMs = 60
}

// 1. 同時開啟 Out4 (Bit 4 = 0x0010, 您實測確認可畫出正方形的標刻/紅光輸出) 與 Out8 (0x0100 RedPt)
previewPort := uint16(0x0110)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(previewPort), 0)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0021, uintptr(previewPort), uintptr(0xFFFF), 0, 0, 0)

// 2. 先定位至左下角起點 c0
d.procGotoXY.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(c0[0]), uintptr(c0[1]))
time.Sleep(15 * time.Millisecond)

// 3. 持續循環執行 6 圈 FPGA 清單，並精確等待物理時間跑完，絕不中途 ResetList！
for !d.stopFlag.Load() && time.Now().Unix() < d.previewEndUnix.Load() {
d.procResetList.Call(uintptr(unsafe.Pointer(&res)), cardID)
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&cmdBuf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), cardID)

// 分段睡眠等待這 6 圈完整跑完，期間若按停止可隨時跳出
slept := 0
for slept < batchDurationMs && !d.stopFlag.Load() {
time.Sleep(10 * time.Millisecond)
slept += 10
}
d.lastX.Store(uint32(c0[0]))
d.lastY.Store(uint32(c0[1]))
}

// 4. 結束預覽：停止清單、滑回中心點 (0x8000, 0x8000) 並關閉 Out4 恢復原狀態
d.procStopExecute.Call(uintptr(unsafe.Pointer(&res)), cardID)
d.procResetList.Call(uintptr(unsafe.Pointer(&res)), cardID)
homeBuf := makeFpgaCmdBuf(c0[0], c0[1], [][2]uint16{{0x8000, 0x8000}}, 0x082a)
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&homeBuf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), cardID)
time.Sleep(40 * time.Millisecond)

d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(d.outPort), 0)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0021, uintptr(d.outPort), uintptr(0xFFFF), 0, 0, 0)
d.lastX.Store(32768)
d.lastY.Store(32768)
}()

return nil
}