//go:build windows

package main

import (
"fmt"
"os"
"path/filepath"
"runtime"
"sync"
"syscall"
"unsafe"
)

// tagResult 對應 LMCMIO.dll 回傳的 6 bytes 結構 (預留 8 個 uint16 避免 VC++ 指標溢位)
type tagResult struct {
v0, v1, v2, v3, v4, v5, v6, v7 uint16
}

type WindowsJczDriver struct {
mu            sync.Mutex
ezcadDir      string
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

func (d *WindowsJczDriver) prepareEnv() error {
if runtime.GOARCH != "386" {
return fmt.Errorf("LMCMIO.dll 為 32 位元動態庫，請使用 GOARCH=386 編譯 (目前為 %s)", runtime.GOARCH)
}
if err := os.Chdir(d.ezcadDir); err != nil {
return fmt.Errorf("無法切換至 EzCad 目錄 (%s): %v", d.ezcadDir, err)
}
return d.dll.Load()
}

func (d *WindowsJczDriver) GetStatus() BoardStatus {
d.mu.Lock()
defer d.mu.Unlock()

status := BoardStatus{
Platform: fmt.Sprintf("Windows (%s)", runtime.GOARCH),
}

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

// 1. MIO_GetState (IOCTL 0x99982010)
d.procGetState.Call(uintptr(unsafe.Pointer(&res)), cardID)
status.StateHex = fmt.Sprintf("0x%04X / 0x%04X / 0x%04X", res.v0, res.v1, res.v2)

// 2. MIO_GetVersion (Cmd 0x0007)
d.procGetVer.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)
status.FirmwareVer = fmt.Sprintf("0x%04X (Sub: 0x%04X)", res.v1, res.v2)

// 3. MIO_GetSerialNo (Cmd 0x0009)
d.procGetSN.Call(uintptr(unsafe.Pointer(&res)), cardID)
status.SerialNo = fmt.Sprintf("%d-%d", res.v1, res.v2)

// 4. MIO_GetChipIDState
var chipState uint16
chipRet, _, _ := d.procChipID.Call(cardID, uintptr(unsafe.Pointer(&chipState)))
status.ChipIDState = fmt.Sprintf("Ret=%d | State=0x%04X", int32(chipRet), chipState)

// 5. GetListStatus (Cmd 0x000a)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x000a, 0, 0, 0, 0, 0)
status.ListReady = (res.v0 == 1)
status.ListStateHex = fmt.Sprintf("0x%04X", res.v0)

// 6. GetPositionXY (Cmd 0x000c)
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x000c, 0, 0, 0, 0, 0)
status.GalvoX = res.v1
status.GalvoY = res.v2

// 7. MIO_ReadPort (Cmd 0x0025)
d.procReadPort.Call(uintptr(unsafe.Pointer(&res)), cardID)
ioVal := res.v1
status.InputPortHex = fmt.Sprintf("0x%04X", ioVal)
status.InputPortBin = fmt.Sprintf("%016b", ioVal)
status.LaserReady = (ioVal & (1 << 0)) != 0
status.XOrg0 = (ioVal & (1 << 14)) != 0
status.YOrg0 = (ioVal & (1 << 15)) != 0

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

var res tagResult
d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(mask), uintptr(val))
return nil
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
d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(x), uintptr(y), 0, 0, 0)
return nil
}