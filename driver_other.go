//go:build !windows

package main

import (
"fmt"
"runtime"
"sync"
"time"
)

type SimulatedJczDriver struct {
mu         sync.Mutex
galvoX     uint16
galvoY     uint16
inPort     uint16
outPort    uint16
previewEnd int64
}

func NewLaserController(ezcadDir string) LaserController {
return &SimulatedJczDriver{
galvoX:  32768,
galvoY:  32768,
inPort:  0xC01F,
outPort: 0x0010,
}
}

func (d *SimulatedJczDriver) GetStatus() BoardStatus {
d.mu.Lock()
defer d.mu.Unlock()
rem := int(d.previewEnd - time.Now().Unix())
active := rem > 0
if !active {
rem = 0
}
return BoardStatus{
Platform:         fmt.Sprintf("%s (%s) [Simulation Mode]", runtime.GOOS, runtime.GOARCH),
Connected:        true,
HandleHex:        "0xSIM01",
StateHex:         "0x0000 / 0x0220 / 0xED90",
FirmwareVer:      "0x8423 (Sub: 0x0009)",
SerialNo:         "7-6",
ChipIDState:      "Ret=0 | State=0x0000",
ListReady:        true,
ListStateHex:     "0x0001",
GalvoX:           d.galvoX,
GalvoY:           d.galvoY,
InputPortHex:     fmt.Sprintf("0x%04X", d.inPort),
InputPortBin:     fmt.Sprintf("%016b", d.inPort),
OutputPortHex:    fmt.Sprintf("0x%04X", d.outPort),
OutputPortBin:    fmt.Sprintf("%016b", d.outPort),
FieldSizeMM:      "110 x 110 mm (Raycus Fiber 20-80kHz)",
TotalParts:       "34957",
TotalTimeSec:     "22009",
HeadUnlocked:     true,
ButtonPressed:    false,
PreviewActive:    active,
PreviewRemainSec: rem,
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

func (d *SimulatedJczDriver) RedLightPreview(sizeMM float64, speedParam int) error {
return d.StartShapePreview("square", sizeMM, speedParam, 3)
}

func (d *SimulatedJczDriver) StartContinuousPreview(sizeMM float64, speedParam int, timeoutSec int) error {
return d.StartShapePreview("square", sizeMM, speedParam, timeoutSec)
}

func (d *SimulatedJczDriver) StartShapePreview(shape string, sizeMM float64, speedParam int, timeoutSec int) error {
d.mu.Lock()
defer d.mu.Unlock()
if timeoutSec <= 0 {
timeoutSec = 30
}
d.previewEnd = time.Now().Add(time.Duration(timeoutSec) * time.Second).Unix()
return nil
}

func (d *SimulatedJczDriver) StopPreview() error {
d.mu.Lock()
defer d.mu.Unlock()
d.previewEnd = 0
return nil
}

func (d *SimulatedJczDriver) ExecuteLaserMark(req MarkRequest) error {
return nil
}