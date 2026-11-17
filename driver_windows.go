//go:build windows

package main

import (
"bufio"
"fmt"
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
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(x), uintptr(y), 0, 0, 0)
return nil
}

// RedLightPreview 100% 還原首次成功畫出正方形的 4 頂點跳轉結構
func (d *WindowsJczDriver) RedLightPreview(sizeMM float64, cornerDelayMs int) error {
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
sizeMM = 15
}
if sizeMM > 90 {
sizeMM = 90
}
if cornerDelayMs < 15 || cornerDelayMs > 200 {
cornerDelayMs = 35
}
halfSpan := uint16((sizeMM / 2.0) * (65535.0 / 110.0))

var res tagResult
previewPort := d.outPort | (1 << 4) | (1 << 8)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(previewPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(previewPort), uintptr(0xFFFF), 0, 0, 0)

c := uint16(32768)
pts := [][2]uint16{
{c - halfSpan, c - halfSpan},
{c + halfSpan, c - halfSpan},
{c + halfSpan, c + halfSpan},
{c - halfSpan, c + halfSpan},
{c - halfSpan, c - halfSpan},
}

delay := time.Duration(cornerDelayMs) * time.Millisecond
for loop := 0; loop < 3; loop++ {
for _, pt := range pts {
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(pt[0]), uintptr(pt[1]), 0, 0, 0)
time.Sleep(delay)
}
}

d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(c), uintptr(c), 0, 0, 0)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(d.outPort), uintptr(0xFFFF), 0, 0, 0)
return nil
}

func (d *WindowsJczDriver) StopPreview() error {
d.stopFlag.Store(true)
return nil
}

// StartContinuousPreview 使用完全相同的 4 頂點跳轉結構進行 30 秒持續循環預覽
func (d *WindowsJczDriver) StartContinuousPreview(sizeMM float64, cornerDelayMs int, timeoutSec int) error {
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
if cornerDelayMs < 15 || cornerDelayMs > 200 {
cornerDelayMs = 35
}
if timeoutSec <= 0 || timeoutSec > 120 {
timeoutSec = 30
}

halfSpan := uint16((sizeMM / 2.0) * (65535.0 / 110.0))
d.stopFlag.Store(false)
d.previewing.Store(true)
d.previewEndUnix.Store(time.Now().Add(time.Duration(timeoutSec) * time.Second).Unix())

go func() {
defer d.previewing.Store(false)

c := uint16(32768)
pts := [][2]uint16{
{c - halfSpan, c - halfSpan},
{c + halfSpan, c - halfSpan},
{c + halfSpan, c + halfSpan},
{c - halfSpan, c + halfSpan},
{c - halfSpan, c - halfSpan},
}
delay := time.Duration(cornerDelayMs) * time.Millisecond

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
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(previewPort), uintptr(0xFFFF), 0, 0, 0)

// 每次鎖定連畫 2 圈完整正方形 (4 頂點直接跳轉)
for loop := 0; loop < 2 && !d.stopFlag.Load(); loop++ {
for _, pt := range pts {
if d.stopFlag.Load() {
break
}
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(pt[0]), uintptr(pt[1]), 0, 0, 0)
time.Sleep(delay)
}
}
d.procClose.Call()
}
}
d.mu.Unlock()
time.Sleep(20 * time.Millisecond)
}

// 預覽結束：回到中心點並關閉紅光輸出
d.mu.Lock()
if err := d.prepareEnv(); err == nil {
ret, _, _ := d.procOpenNew.Call()
if int32(ret) <= 0 {
d.procOpen.Call(0)
}
if h, _, _ := d.procGetHandle.Call(0); h != 0 {
var res tagResult
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(c), uintptr(c), 0, 0, 0)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(d.outPort), uintptr(0xFFFF), 0, 0, 0)
d.procClose.Call()
}
}
d.mu.Unlock()
}()

return nil
}