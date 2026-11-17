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

// 256 個 Slot x 6 個 uint16 = 1536 uint16 (3072 Bytes)
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
procCmd         *syscall.LazyProc
procChipID      *syscall.LazyProc
procClose       *syscall.LazyProc
}

func NewLaserController(ezcadDir string) LaserController {
dllPath := filepath.Join(ezcadDir, "LMCMIO.dll")
dll := syscall.NewLazyDLL(dllPath)

d := &WindowsJczDriver{
ezcadDir:        ezcadDir,
outPort:         0x0010, // 原廠待機狀態: 0x0010 (Out4 Active-Low 預設高電位關閉)
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
// 預覽期間直接回傳快取與倒數秒數，絕不干擾控制卡 USB 串流
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

// buildFirstPreviewBuf 100% 複製 Frida 攔截到的原廠第 1 個 WriteCmdBuf：
// Slot 0: 0x8051 (0)
// Slot 1: 0x8006 (jumpSpeed = 0x082a)
// Slot 2: 0x800c (markSpeed = 0x082a)
// Slot 3: 0x8007 (0)
// Slot 4: 0x8008 (0)
// Slot 5: 0x800f (0)
// Slot 6: 0x800d (jumpDelay = 0，轉角零停頓無聲滑過！)
// Slot 7 ~ 255: 全部填滿 0x8001 正方形四頂點循環 (絕不放 0x8002 EndOfList！)
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

// buildContPreviewBuf 100% 複製 Frida 攔截到的原廠第 2 個以後的 WriteCmdBuf：
// Slot 0 ~ Slot 255 全部 256 個槽位通通填滿 0x8001 (共 64 圈正方形)，無縫銜接上一塊 Buffer！
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

// buildMoveSingleBuf 100% 複製 Frida 攔截到的原廠停止歸零清單 (Slot 0~3 + Slot 4 起全填 0x8002)
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
buf.words[2*6+1] = 25 // 0x0019 (原廠收尾固定用 p1=25)
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

// StartContinuousPreview 100% 複製 Frida 攔截到的原廠 F1 紅光預覽流程：
// 1. WritePort(0x110, 0) -> ResetList() -> GotoXY(0x8001, 0x8001)
// 2. WriteCmdBuf(firstBuf) -> WriteCmdBuf(contBuf) -> ExecuteList()
// 3. 迴圈中當 GetListStatus 可寫入時持續補充 WriteCmdBuf(contBuf)，中間絕不呼叫 ResetList！
// 4. 停止時：StopExecute() -> WritePort(0x110, 0) -> ResetList() -> GotoXY(curX, curY) -> WriteCmdBuf(回 0x8000, 0x8000) -> ExecuteList() -> SetControlMode(1) -> SetStandby(2000, 20, 1) -> WritePort(0x10, 0)
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
jumpSpeedWord = 0x082a // 原廠預設 2090 (0x082a)
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

// 完全比照原廠頂點順序：左上 -> 右上 -> 右下 -> 左下
corners := [4][2]uint16{
{c - halfSpan, c + halfSpan},
{c + halfSpan, c + halfSpan},
{c + halfSpan, c - halfSpan},
{c - halfSpan, c - halfSpan},
}

// 預先建立第 1 個 Buffer 與後續無縫接續的 4 種相位 Buffer
firstBuf, nextIdx, lastX, lastY := buildFirstPreviewBuf(0x8001, 0x8001, corners, jumpSpeedWord)
contBuf, _, _, _ := buildContPreviewBuf(lastX, lastY, corners, nextIdx)

// === 原廠啟動序列 ===
// [MIO_WritePort] arg1=0x110 arg2=0x0
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x0110, 0)
// [MIO_ResetList] (全場唯一一次 ResetList！)
d.procResetList.Call(uintptr(unsafe.Pointer(&res)), cardID)
// [MIO_GotoXY] X=32769(0x8001) Y=32769(0x8001)
d.procGotoXY.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x8001, 0x8001)

// 寫入第 1 塊與第 2 塊 Buffer 填滿雙緩衝區，然後啟動 ExecuteList！
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&firstBuf)))
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&contBuf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), cardID)

// === 原廠無縫串流迴圈 (中間絕不呼叫 ResetList，也不呼叫 ExecuteList) ===
for !d.stopFlag.Load() && time.Now().Unix() < d.previewEndUnix.Load() {
d.procGetListStat.Call(uintptr(unsafe.Pointer(&res)), cardID)
// 當雙緩衝區空出一個槽位 (res.v0 != 0) 時，立即補入下一塊 64 圈 Buffer
if res.v0 != 0 {
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&contBuf)))
} else {
time.Sleep(5 * time.Millisecond)
}
}

// === 原廠停止序列 (與 Frida 攔截紀錄 100% 相同) ===
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
// 恢復待機狀態 arg1=0x10 arg2=0x0
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(d.outPort), 0)

d.lastX.Store(32768)
d.lastY.Store(32768)
}()

return nil
}