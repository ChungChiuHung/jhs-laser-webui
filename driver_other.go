//go:build !windows

package main

import (
"fmt"
"runtime"
"sync"
)

type SimulatedJczDriver struct {
mu      sync.Mutex
galvoX  uint16
galvoY  uint16
inPort  uint16
outPort uint16
}

func NewLaserController(ezcadDir string) LaserController {
return &SimulatedJczDriver{
galvoX: 32768,
galvoY: 32768,
inPort: 0xC01F,
}
}

func (d *SimulatedJczDriver) GetStatus() BoardStatus {
d.mu.Lock()
defer d.mu.Unlock()

return BoardStatus{
Platform:      fmt.Sprintf("%s (%s) [Simulation Mode]", runtime.GOOS, runtime.GOARCH),
Connected:     true,
HandleHex:     "0xSIM01",
StateHex:      "0x0000 / 0x0220 / 0xED90",
FirmwareVer:   "0x8423 (Sub: 0x0009)",
SerialNo:      "7-6",
ChipIDState:   "Ret=0 | State=0x0000",
ListReady:     true,
ListStateHex:  "0x0001",
GalvoX:        d.galvoX,
GalvoY:        d.galvoY,
InputPortHex:  fmt.Sprintf("0x%04X", d.inPort),
InputPortBin:  fmt.Sprintf("%016b", d.inPort),
OutputPortHex: fmt.Sprintf("0x%04X", d.outPort),
OutputPortBin: fmt.Sprintf("%016b", d.outPort),
FieldSizeMM:   "110 x 110 mm (Raycus Fiber 20-80kHz)",
TotalParts:    "34957",
TotalTimeSec:  "22009",
}
}

func (d *SimulatedJczDriver) WriteOutputPort(mask uint16, val uint16) error {
d.mu.Lock()
defer d.mu.Unlock()
d.outPort = (d.outPort & ^mask) | (val & mask)
return nil
}

func (d *SimulatedJczDriver) GotoXY(x uint16, y uint16) error {
d.mu.Lock()
defer d.mu.Unlock()
d.galvoX = x
d.galvoY = y
return nil
}