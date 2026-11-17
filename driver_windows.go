//go:build windows

package main

import (
.\.git\"fmt"
.\.git\"os"
.\.git\"path/filepath"
.\.git\"runtime"
.\.git\"sync"
.\.git\"syscall"
.\.git\"unsafe"
)

type tagResult struct {
.\.git\v0, v1, v2, v3, v4, v5, v6, v7 uint16
}

type WindowsJczDriver struct {
.\.git\mu            sync.Mutex
.\.git\ezcadDir      string
.\.git\outPort       uint16
.\.git\dll           *syscall.LazyDLL
.\.git\procOpenNew   *syscall.LazyProc
.\.git\procOpen      *syscall.LazyProc
.\.git\procGetHandle *syscall.LazyProc
.\.git\procGetState  *syscall.LazyProc
.\.git\procGetVer    *syscall.LazyProc
.\.git\procGetSN     *syscall.LazyProc
.\.git\procReadPort  *syscall.LazyProc
.\.git\procWritePort *syscall.LazyProc
.\.git\procCmd       *syscall.LazyProc
.\.git\procChipID    *syscall.LazyProc
.\.git\procClose     *syscall.LazyProc
}

func NewLaserController(ezcadDir string) LaserController {
.\.git\dllPath := filepath.Join(ezcadDir, "LMCMIO.dll")
.\.git\dll := syscall.NewLazyDLL(dllPath)

.\.git\return &WindowsJczDriver{
.\.github\ezcadDir:      ezcadDir,
.\.github\dll:           dll,
.\.github\procOpenNew:   dll.NewProc("?MIO_OpenNewDev@@YAHXZ"),
.\.github\procOpen:      dll.NewProc("?MIO_Open@@YAHH@Z"),
.\.github\procGetHandle: dll.NewProc("?MIO_GetDevHandle@@YAPAXH@Z"),
.\.github\procGetState:  dll.NewProc("?MIO_GetState@@YA?AUtagResult@@E@Z"),
.\.github\procGetVer:    dll.NewProc("?MIO_GetVersion@@YA?AUtagResult@@EG@Z"),
.\.github\procGetSN:     dll.NewProc("?MIO_GetSerialNo@@YA?AUtagResult@@E@Z"),
.\.github\procReadPort:  dll.NewProc("?MIO_ReadPort@@YA?AUtagResult@@E@Z"),
.\.github\procWritePort: dll.NewProc("?MIO_WritePort@@YA?AUtagResult@@EGG@Z"),
.\.github\procCmd:       dll.NewProc("?MIO_Cmd@@YA?AUtagResult@@EGGGGGG@Z"),
.\.github\procChipID:    dll.NewProc("?MIO_GetChipIDState@@YAHEAAG@Z"),
.\.github\procClose:     dll.NewProc("?MIO_Close@@YAXXZ"),
.\.git\}
}

func (d *WindowsJczDriver) prepareEnv() error {
.\.git\if runtime.GOARCH != "386" {
.\.github\return fmt.Errorf("LMCMIO.dll 為 32 位元動態庫，請使用 GOARCH=386 編譯 (目前為 %s)", runtime.GOARCH)
.\.git\}
.\.git\if err := os.Chdir(d.ezcadDir); err != nil {
.\.github\return fmt.Errorf("無法切換至 EzCad 目錄 (%s): %v", d.ezcadDir, err)
.\.git\}
.\.git\return d.dll.Load()
}

func (d *WindowsJczDriver) GetStatus() BoardStatus {
.\.git\d.mu.Lock()
.\.git\defer d.mu.Unlock()

.\.git\status := BoardStatus{
.\.github\Platform: fmt.Sprintf("Windows (%s)", runtime.GOARCH),
.\.git\}

.\.git\if err := d.prepareEnv(); err != nil {
.\.github\status.ErrorMsg = err.Error()
.\.github\return status
.\.git\}

.\.git\ret, _, _ := d.procOpenNew.Call()
.\.git\if int32(ret) <= 0 {
.\.github\ret, _, _ = d.procOpen.Call(0)
.\.git\}

.\.git\handle, _, _ := d.procGetHandle.Call(0)
.\.git\if handle == 0 {
.\.github\status.ErrorMsg = "無法取得控制卡 Handle (請確認 USB 連線正常且 EzCad2.exe 已關閉)"
.\.github\return status
.\.git\}
.\.git\defer d.procClose.Call()

.\.git\status.Connected = true
.\.git\status.HandleHex = fmt.Sprintf("0x%X", handle)

.\.git\var res tagResult
.\.git\cardID := uintptr(0)

.\.git\d.procGetState.Call(uintptr(unsafe.Pointer(&res)), cardID)
.\.git\status.StateHex = fmt.Sprintf("0x%04X / 0x%04X / 0x%04X", res.v0, res.v1, res.v2)

.\.git\d.procGetVer.Call(uintptr(unsafe.Pointer(&res)), cardID, 1)
.\.git\status.FirmwareVer = fmt.Sprintf("0x%04X (Sub: 0x%04X)", res.v1, res.v2)

.\.git\d.procGetSN.Call(uintptr(unsafe.Pointer(&res)), cardID)
.\.git\status.SerialNo = fmt.Sprintf("%d-%d", res.v1, res.v2)

.\.git\var chipState uint16
.\.git\chipRet, _, _ := d.procChipID.Call(cardID, uintptr(unsafe.Pointer(&chipState)))
.\.git\status.ChipIDState = fmt.Sprintf("Ret=%d | State=0x%04X", int32(chipRet), chipState)

.\.git\d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x000a, 0, 0, 0, 0, 0)
.\.git\status.ListReady = (res.v0 == 1)
.\.git\status.ListStateHex = fmt.Sprintf("0x%04X", res.v0)

.\.git\d.procCmd.Call(uintptr(unsafe.Pointer(&res)), cardID, 0x000c, 0, 0, 0, 0, 0)
.\.git\status.GalvoX = res.v1
.\.git\status.GalvoY = res.v2

.\.git\d.procReadPort.Call(uintptr(unsafe.Pointer(&res)), cardID)
.\.git\ioVal := res.v1
.\.git\status.InputPortHex = fmt.Sprintf("0x%04X", ioVal)
.\.git\status.InputPortBin = fmt.Sprintf("%016b", ioVal)
.\.git\status.OutputPortHex = fmt.Sprintf("0x%04X", d.outPort)
.\.git\status.OutputPortBin = fmt.Sprintf("%016b", d.outPort)
.\.git\status.LaserReady = (ioVal & (1 << 0)) != 0
.\.git\status.XOrg0 = (ioVal & (1 << 14)) != 0
.\.git\status.YOrg0 = (ioVal & (1 << 15)) != 0

.\.git\return status
}

func (d *WindowsJczDriver) WriteOutputPort(mask uint16, val uint16) error {
.\.git\d.mu.Lock()
.\.git\defer d.mu.Unlock()

.\.git\if err := d.prepareEnv(); err != nil {
.\.github\return err
.\.git\}
.\.git\ret, _, _ := d.procOpenNew.Call()
.\.git\if int32(ret) <= 0 {
.\.github\d.procOpen.Call(0)
.\.git\}
.\.git\handle, _, _ := d.procGetHandle.Call(0)
.\.git\if handle == 0 {
.\.github\return fmt.Errorf("無法開啟控制卡")
.\.git\}
.\.git\defer d.procClose.Call()

.\.git\// 根據位元遮罩更新目標輸出狀態
.\.git\d.outPort = (d.outPort & ^mask) | (val & mask)

.\.git\var res tagResult
.\.git\// 呼叫 MIO_WritePort (對應單一命令 0x0021)，傳入最新 16-bit 輸出值與遮罩
.\.git\d.procWritePort.Call(uintptr(unsafe.Pointer(&res)), 0, uintptr(d.outPort), uintptr(0xFFFF))
.\.git\return nil
}

func (d *WindowsJczDriver) GotoXY(x uint16, y uint16) error {
.\.git\d.mu.Lock()
.\.git\defer d.mu.Unlock()

.\.git\if err := d.prepareEnv(); err != nil {
.\.github\return err
.\.git\}
.\.git\ret, _, _ := d.procOpenNew.Call()
.\.git\if int32(ret) <= 0 {
.\.github\d.procOpen.Call(0)
.\.git\}
.\.git\handle, _, _ := d.procGetHandle.Call(0)
.\.git\if handle == 0 {
.\.github\return fmt.Errorf("無法開啟控制卡")
.\.git\}
.\.git\defer d.procClose.Call()

.\.git\var res tagResult
.\.git\d.procCmd.Call(uintptr(unsafe.Pointer(&res)), 0, 0x000d, uintptr(x), uintptr(y), 0, 0, 0)
.\.git\return nil
}