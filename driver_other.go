//go:build !windows

package main

import (
.\.git\"fmt"
.\.git\"runtime"
.\.git\"sync"
)

type SimulatedJczDriver struct {
.\.git\mu      sync.Mutex
.\.git\galvoX  uint16
.\.git\galvoY  uint16
.\.git\inPort  uint16
.\.git\outPort uint16
}

func NewLaserController(ezcadDir string) LaserController {
.\.git\return &SimulatedJczDriver{
.\.github\galvoX: 32768,
.\.github\galvoY: 32768,
.\.github\inPort: 0xC01F,
.\.git\}
}

func (d *SimulatedJczDriver) GetStatus() BoardStatus {
.\.git\d.mu.Lock()
.\.git\defer d.mu.Unlock()

.\.git\return BoardStatus{
.\.github\Platform:      fmt.Sprintf("%s (%s) [Simulation Mode]", runtime.GOOS, runtime.GOARCH),
.\.github\Connected:     true,
.\.github\HandleHex:     "0xSIM01",
.\.github\StateHex:      "0x0000 / 0x0220 / 0xED90",
.\.github\FirmwareVer:   "0x8423 (Sub: 0x0009)",
.\.github\SerialNo:      "7-6",
.\.github\ChipIDState:   "Ret=0 | State=0x0000",
.\.github\ListReady:     true,
.\.github\ListStateHex:  "0x0001",
.\.github\GalvoX:        d.galvoX,
.\.github\GalvoY:        d.galvoY,
.\.github\InputPortHex:  fmt.Sprintf("0x%04X", d.inPort),
.\.github\InputPortBin:  fmt.Sprintf("%016b", d.inPort),
.\.github\OutputPortHex: fmt.Sprintf("0x%04X", d.outPort),
.\.github\OutputPortBin: fmt.Sprintf("%016b", d.outPort),
.\.github\LaserReady:    (d.inPort & (1 << 0)) != 0,
.\.github\XOrg0:         (d.inPort & (1 << 14)) != 0,
.\.github\YOrg0:         (d.inPort & (1 << 15)) != 0,
.\.git\}
}

func (d *SimulatedJczDriver) WriteOutputPort(mask uint16, val uint16) error {
.\.git\d.mu.Lock()
.\.git\defer d.mu.Unlock()
.\.git\d.outPort = (d.outPort & ^mask) | (val & mask)
.\.git\return nil
}

func (d *SimulatedJczDriver) GotoXY(x uint16, y uint16) error {
.\.git\d.mu.Lock()
.\.git\defer d.mu.Unlock()
.\.git\d.galvoX = x
.\.git\d.galvoY = y
.\.git\return nil
}