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

// 原廠 tagLmcCmd 清單緩衝區：固定 256 條指令 x 6 個 uint16 = 1536 uint16 (3072 Bytes)
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
outPort:         0x0010, // 原廠待機預設值 arg1=0x10 (Out4 Active-Low 預設高電位關閉)
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

return status
}

// WriteOutputPort 依照 Frida 攔截到的原廠格式: MIO_WritePort(card=0, arg1=portVal, arg2=0)
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
// 原廠格式：arg2 傳 0
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), 0)
return nil
}

// buildAndRunFpgaList 100% 複製原廠 EzCad2 的 WriteCmdBuf 硬體插補清單結構！
// 每條指令 6 個 uint16:
//   [0] 0x8051, 0, 0, 0, 0, 0
//   [1] 0x8006, jumpSpeed (0x082a), 0, 0, 0, 0
//   [2] 0x800d, jumpDelay (0x0008), 0, 0, 0, 0
//   [3..N] 0x8001, targetX, targetY, 0, distance, 0
//   [餘數全部填滿] 0x8002, 0, 0, 0, 0, 0 (EndOfList)
func (d *WindowsJczDriver) buildAndRunFpgaList(startX, startY uint16, points [][2]uint16, jumpSpeedWord uint16) {
var res tagResult
cardID := uintptr(0)

// 1. 原廠步驟 1: MIO_ResetList (0x0012)
d.procResetList.Call(uintptr(unsafe.Pointer(&res)), cardID)

// 2. 原廠步驟 2: 先呼叫 MIO_GotoXY 定位到起點
d.procGotoXY.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(startX), uintptr(startY))

// 3. 組裝 256 條指令 (1536 uint16) 的 tagLmcCmd 緩衝區
var buf tagLmcCmd
// 先將所有 256 個槽位預設填為 0x8002 (listEndofList)，與 Frida 攔截結果 100% 一致
for slot := 0; slot < 256; slot++ {
buf.words[slot*6] = 0x8002
}

// Slot 0: 0x8051
buf.words[0] = 0x8051
// Slot 1: 0x8006 (listJumpSpeed, 原廠預設 0x082a = 2090)
if jumpSpeedWord == 0 {
jumpSpeedWord = 0x082a
}
buf.words[6] = 0x8006
buf.words[7] = jumpSpeedWord
// Slot 2: 0x800d (listJumpDelay, 原廠預設 0x0008)
buf.words[12] = 0x800d
buf.words[13] = 0x0008

// Slot 3 起: 寫入每一筆 0x8001 (listJumpTo) 與幾何距離
curX, curY := float64(startX), float64(startY)
slot := 3
for _, pt := range points {
if slot >= 250 {
break
}
tx, ty := float64(pt[0]), float64(pt[1])
dist := uint16(math.Hypot(tx-curX, ty-curY))
if dist == 0 {
dist = 1
}
base := slot * 6
buf.words[base+0] = 0x8001 // listJumpTo
buf.words[base+1] = pt[0]  // X 座標
buf.words[base+2] = pt[1]  // Y 座標
buf.words[base+3] = 0x0000
buf.words[base+4] = dist   // 原廠關鍵第 5 參數：幾何移動距離，供 FPGA 計算無聲插補時間！
buf.words[base+5] = 0x0000
curX, curY = tx, ty
slot++
}

// 4. 原廠步驟 4: MIO_WriteCmdBuf 寫入 FPGA + MIO_ExecuteList (0x0005) 執行！
d.procWriteCmdBuf.Call(uintptr(unsafe.Pointer(&res)), cardID, uintptr(unsafe.Pointer(&buf)))
d.procExecuteList.Call(uintptr(unsafe.Pointer(&res)), cardID)

// 5. 等待 FPGA 執行完畢 (輪詢 MIO_Cmd 0x0025 或 MIO_GetState，最多等待 800ms)
for wait := 0; wait < 80; wait++ {
time.Sleep(10 * time.Millisecond)
d.procGetState.Call(uintptr(unsafe.Pointer(&res)), cardID)
// 當 FPGA 清單執行完畢時跳出
if (res.v1 & 0x0400) == 0 {
break
}
}
}

// GotoXY 100% 複製 Frida 攔截到的原廠「回中心點 / 單點無聲移動」流程！
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

// 使用原廠 FPGA 清單無聲滑行到目標點 (x, y)
pts := [][2]uint16{{x, y}}
d.buildAndRunFpgaList(curX, curY, pts, 0x082a)
d.lastX.Store(uint32(x))
d.lastY.Store(uint32(y))
return nil
}

// RedLightPreview 使用原廠 WriteCmdBuf 硬體插補清單無聲掃描正方形 5 圈
func (d *WindowsJczDriver) RedLightPreview(sizeMM float64, speedParam int) error {
return d.StartContinuousPreview(sizeMM, speedParam, 3)
}

func (d *WindowsJczDriver) StopPreview() error {
d.stopFlag.Store(true)
return nil
}

// StartContinuousPreview 使用原廠 WriteCmdBuf + ExecuteList 硬體清單實現 100% 無聲正方形預覽
func (d *WindowsJczDriver) StartContinuousPreview(sizeMM float64, speedParam int, timeoutSec int) error {
if d.previewing.Load() {
if timeoutSec <= 0 || timeoutSec > 120 {
timeoutSec = 30
}
d.previewEndUnix.Store(time.Now().Add(time.Duration(timeoutSec) * time.Second).Unix())
return nil
}

if sizeMM < 2 {
sizeMM = 25
}
if sizeMM > 90 {
sizeMM = 90
}
if timeoutSec <= 0 || timeoutSec > 120 {
timeoutSec = 30
}
jumpSpeedWord := uint16(speedParam)
if jumpSpeedWord < 200 || jumpSpeedWord > 10000 {
jumpSpeedWord = 0x082a // 原廠預設值 2090 (對應 3000mm/s)
}

halfSpan := uint16((sizeMM / 2.0) * (65535.0 / 110.0))
d.stopFlag.Store(false)
d.previewing.Store(true)
d.previewEndUnix.Store(time.Now().Add(time.Duration(timeoutSec) * time.Second).Unix())

go func() {
defer d.previewing.Store(false)

c := uint16(32768)
c0 := [2]uint16{c - halfSpan, c - halfSpan}
c1 := [2]uint16{c + halfSpan, c - halfSpan}
c2 := [2]uint16{c + halfSpan, c + halfSpan}
c3 := [2]uint16{c - halfSpan, c + halfSpan}

// 一次在 FPGA 清單中塞入 10 圈正方形 (共 40 條 0x8001 指令)，讓 FPGA 自己連續無聲插補！
var tenLoops [][2]uint16
for l := 0; l < 10; l++ {
tenLoops = append(tenLoops, c1, c2, c3, c0)
}

for !d.stopFlag.Load() && time.Now().Unix() < d.previewEndUnix.Load() {
d.mu.Lock()
if err := d.prepareEnv(); err == nil {
ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
d.procOpen.Call(0)
}
if h, _, _ := d.procGetHandle.Call(0); h != 0 {
var res tagResult
// 同時開啟 Out8 (0x0100 RedPt) 與 Out4 (0x0010)，完全比照原廠 0x0110
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(0x0110), 0)
d.buildAndRunFpgaList(c0[0], c0[1], tenLoops, jumpSpeedWord)
d.procClose.Call()
}
}
d.mu.Unlock()
time.Sleep(10 * time.Millisecond)
}

// 預覽結束：完全比照 Frida 攔截到的原廠停止序列！
// StopExecute (0x001f) -> WritePort(0x110) -> ResetList -> GotoXY -> WriteCmdBuf(回 0x8000,0x8000) -> ExecuteList -> WritePort(0x10)
d.mu.Lock()
if err := d.prepareEnv(); err == nil {
ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
d.procOpen.Call(0)
}
if h, _, _ := d.procGetHandle.Call(0); h != 0 {
var res tagResult
d.procStopExecute.Call(uintptr(unsafe.Pointer(&res)), 0)
d.buildAndRunFpgaList(c0[0], c0[1], [][2]uint16{{0x8000, 0x8000}}, 0x082a)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), 0)
d.procClose.Call()
}
}
d.mu.Unlock()
}()

return nil
}