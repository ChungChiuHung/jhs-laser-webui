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

// smoothMove 採用線性微步插補將振鏡平滑移動至目標點，避免大跨距瞬間跳躍造成振鏡馬達異音
func (d *WindowsJczDriver) smoothMove(fromX, fromY, toX, toY uint16, steps int, delay time.Duration) {
var res tagResult
dx := (float64(toX) - float64(fromX)) / float64(steps)
dy := (float64(toY) - float64(fromY)) / float64(steps)
for s := 1; s <= steps; s++ {
nx := uint16(float64(fromX) + dx*float64(s))
ny := uint16(float64(fromY) + dy*float64(s))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(nx), uintptr(ny), 0, 0, 0)
if delay > 0 {
time.Sleep(delay)
}
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
d.smoothMove(curX, curY, x, y, 25, time.Millisecond)
return nil
}

// RedLightPreview 同步開啟 Out4 (標刻紅光輸出) 與 Out8，並以微步插補靜音描繪正方形外框
func (d *WindowsJczDriver) RedLightPreview(sizeMM float64) error {
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

// 限制預覽尺寸在 2mm ~ 90mm 安全範圍內 (全幅面 110mm = 65535 DAC, 1mm ≈ 595.77 DAC)
if sizeMM < 2 {
sizeMM = 20
}
if sizeMM > 90 {
sizeMM = 90
}
halfSpan := uint16((sizeMM / 2.0) * (65535.0 / 110.0))

var res tagResult
// 1. 同時開啟 Out4 (MarkOut 紅光連動向) 與 Out8 (RedPt)
previewPort := d.outPort | (1 << 4) | (1 << 8)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(previewPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(previewPort), uintptr(0xFFFF), 0, 0, 0)

c := uint16(32768)
corners := [][2]uint16{
{c - halfSpan, c - halfSpan},
{c + halfSpan, c - halfSpan},
{c + halfSpan, c + halfSpan},
{c - halfSpan, c + halfSpan},
{c - halfSpan, c - halfSpan},
}

// 先從中心點平滑移動至起點角
d.smoothMove(c, c, corners[0][0], corners[0][1], 20, time.Millisecond)

// 根據邊長動態決定插補步數 (每步約 0.6mm，確保高速且安靜無聲)
stepsPerEdge := int(sizeMM / 0.6)
if stepsPerEdge < 15 {
stepsPerEdge = 15
}
if stepsPerEdge > 80 {
stepsPerEdge = 80
}

// 2. 靜音平滑掃描 3 圈正方形
for loop := 0; loop < 3; loop++ {
for i := 0; i < 4; i++ {
d.smoothMove(corners[i][0], corners[i][1], corners[i+1][0], corners[i+1][1], stepsPerEdge, time.Millisecond)
}
}

// 3. 平滑回到中心點 (32768, 32768) 並還原原本的 OutPort 狀態
d.smoothMove(corners[4][0], corners[4][1], c, c, 20, time.Millisecond)
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), uintptr(0xFFFF))
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x0021, uintptr(d.outPort), uintptr(0xFFFF), 0, 0, 0)
return nil
}