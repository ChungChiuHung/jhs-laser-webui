package main

import (
"fmt"
"math"
"sort"
"strings"
)

type QRVectorRequest struct {
Text        string  `json:"text"`
SizeMM      float64 `json:"size_mm"`
SizeBasis   string  `json:"size_basis"`
QuietZone   int     `json:"quiet_zone"`
ECC         string  `json:"ecc"`
HatchStepMM float64 `json:"hatch_step_mm"`
ScaleX      float64 `json:"scale_x"`
ScaleY      float64 `json:"scale_y"`
PowerPct    float64 `json:"power_pct"`
SpeedWord   uint16  `json:"speed_word"`
}

type QRVectorResponse struct {
OK           bool           `json:"ok"`
Error        string         `json:"error,omitempty"`
ModuleCount  int            `json:"module_count"`
QuietZone    int            `json:"quiet_zone"`
ECC          string         `json:"ecc"`
OverallMM    float64        `json:"overall_mm"`
CodeMM       float64        `json:"code_mm"`
ModuleMM     float64        `json:"module_mm"`
ContourCount int            `json:"contour_count"`
ContoursMM   [][][2]float64 `json:"contours_mm"`
SVG          string         `json:"svg"`
DXF          string         `json:"dxf"`
}

func fmtFloat(n float64) string {
v := math.Round(n*1000.0) / 1000.0
s := fmt.Sprintf("%.3f", v)
s = strings.TrimRight(s, "0")
s = strings.TrimRight(s, ".")
if s == "" || s == "-0" {
return "0"
}
return s
}

// ============================================================================
// 純 Go 零依賴 ISO/IEC 18004 QRCode 編碼器 (支援 Version 1~10 Byte 模式與 L/M/Q/H 容錯)
// ============================================================================

type qrMatrix struct {
size    int
modules [][]bool
isFunc  [][]bool
}

var gfExp [512]int
var gfLog [256]int

func init() {
x := 1
for i := 0; i < 255; i++ {
gfExp[i] = x
gfLog[x] = i
x <<= 1
if x&0x100 != 0 {
x ^= 0x11d
}
}
for i := 255; i < 512; i++ {
gfExp[i] = gfExp[i-255]
}
}

func gfMul(a, b int) int {
if a == 0 || b == 0 {
return 0
}
return gfExp[gfLog[a]+gfLog[b]]
}

func rsGeneratorPoly(degree int) []int {
poly := []int{1}
for i := 0; i < degree; i++ {
next := make([]int, len(poly)+1)
factor := gfExp[i]
for j := 0; j < len(poly); j++ {
next[j] ^= poly[j]
next[j+1] ^= gfMul(poly[j], factor)
}
poly = next
}
return poly
}

func rsRemainder(data []byte, ecLen int) []byte {
gen := rsGeneratorPoly(ecLen)
rem := make([]int, ecLen)
for _, b := range data {
factor := int(b) ^ rem[0]
copy(rem, rem[1:])
rem[ecLen-1] = 0
for j := 0; j < ecLen; j++ {
rem[j] ^= gfMul(gen[j+1], factor)
}
}
out := make([]byte, ecLen)
for i, v := range rem {
out[i] = byte(v)
}
return out
}

type qrSpec struct {
ver      int
ecc      string
totalCW  int
ecPerBlk int
numBlk1  int
dataCW1  int
numBlk2  int
dataCW2  int
}

// 涵蓋 Version 1 ~ 10 的標準 RS 區塊參數表 (可容納高達 271 Bytes UTF-8 文字/網址)
var qrSpecs = []qrSpec{
{1, "L", 26, 7, 1, 19, 0, 0}, {1, "M", 26, 10, 1, 16, 0, 0}, {1, "Q", 26, 13, 1, 13, 0, 0}, {1, "H", 26, 17, 1, 9, 0, 0},
{2, "L", 44, 10, 1, 34, 0, 0}, {2, "M", 44, 16, 1, 28, 0, 0}, {2, "Q", 44, 22, 1, 22, 0, 0}, {2, "H", 44, 28, 1, 16, 0, 0},
{3, "L", 70, 15, 1, 55, 0, 0}, {3, "M", 70, 26, 1, 44, 0, 0}, {3, "Q", 70, 18, 2, 17, 0, 0}, {3, "H", 70, 22, 2, 13, 0, 0},
{4, "L", 100, 20, 1, 80, 0, 0}, {4, "M", 100, 18, 2, 32, 0, 0}, {4, "Q", 100, 26, 2, 24, 0, 0}, {4, "H", 100, 16, 4, 9, 0, 0},
{5, "L", 134, 26, 1, 108, 0, 0}, {5, "M", 134, 24, 2, 43, 0, 0}, {5, "Q", 134, 18, 2, 15, 2, 16}, {5, "H", 134, 22, 2, 11, 2, 12},
{6, "L", 172, 18, 2, 68, 0, 0}, {6, "M", 172, 16, 4, 27, 0, 0}, {6, "Q", 172, 24, 4, 19, 0, 0}, {6, "H", 172, 28, 4, 15, 0, 0},
{7, "L", 196, 20, 2, 78, 0, 0}, {7, "M", 196, 18, 4, 31, 0, 0}, {7, "Q", 196, 18, 2, 14, 4, 15}, {7, "H", 196, 26, 4, 13, 1, 14},
{8, "L", 242, 24, 2, 97, 0, 0}, {8, "M", 242, 22, 2, 38, 2, 39}, {8, "Q", 242, 22, 4, 18, 2, 19}, {8, "H", 242, 26, 4, 14, 2, 15},
{9, "L", 292, 30, 2, 116, 0, 0}, {9, "M", 292, 22, 3, 36, 2, 37}, {9, "Q", 292, 20, 4, 16, 4, 17}, {9, "H", 292, 24, 4, 12, 4, 13},
{10, "L", 346, 18, 2, 68, 2, 69}, {10, "M", 346, 26, 4, 43, 1, 44}, {10, "Q", 346, 24, 6, 19, 2, 20}, {10, "H", 346, 28, 6, 15, 2, 16},
}

var alignCoords = [][]int{
{}, {}, {6, 18}, {6, 22}, {6, 26}, {6, 30}, {6, 34},
{6, 22, 38}, {6, 24, 42}, {6, 26, 46}, {6, 28, 50},
}

func normalizeECC(ecc string) string {
switch strings.ToUpper(strings.TrimSpace(ecc)) {
case "L", "Q", "H":
return strings.ToUpper(strings.TrimSpace(ecc))
default:
return "M"
}
}

func encodePureQR(text, ecc string) (*qrMatrix, string, error) {
ecc = normalizeECC(ecc)
raw := []byte(text)
var chosen *qrSpec
for i := range qrSpecs {
sp := &qrSpecs[i]
if sp.ecc != ecc {
continue
}
capBytes := sp.numBlk1*sp.dataCW1 + sp.numBlk2*sp.dataCW2 - 2
if len(raw) <= capBytes {
chosen = sp
break
}
}
if chosen == nil {
return nil, ecc, fmt.Errorf("文字長度 (%d bytes) 超過 Version 10 (%s 容錯) 上限", len(raw), ecc)
}

totalDataCW := chosen.numBlk1*chosen.dataCW1 + chosen.numBlk2*chosen.dataCW2
var bits []int
pushBits := func(val, n int) {
for i := n - 1; i >= 0; i-- {
bits = append(bits, (val>>i)&1)
}
}
// Byte mode (0100) + 8-bit length (ver 1..9) or 16-bit length (ver 10)
pushBits(0x4, 4)
if chosen.ver <= 9 {
pushBits(len(raw), 8)
} else {
pushBits(len(raw), 16)
}
for _, b := range raw {
pushBits(int(b), 8)
}
for i := 0; i < 4 && len(bits) < totalDataCW*8; i++ {
bits = append(bits, 0)
}
for len(bits)%8 != 0 {
bits = append(bits, 0)
}
dataCW := make([]byte, totalDataCW)
for i := 0; i < len(bits)/8 && i < totalDataCW; i++ {
var v byte
for b := 0; b < 8; b++ {
v = (v << 1) | byte(bits[i*8+b])
}
dataCW[i] = v
}
pads := []byte{0xEC, 0x11}
pIdx := 0
for i := len(bits) / 8; i < totalDataCW; i++ {
dataCW[i] = pads[pIdx%2]
pIdx++
}

// 切割區塊並計算 Reed-Solomon 糾錯碼
numBlocks := chosen.numBlk1 + chosen.numBlk2
dataBlocks := make([][]byte, numBlocks)
ecBlocks := make([][]byte, numBlocks)
offset := 0
for b := 0; b < numBlocks; b++ {
cwLen := chosen.dataCW1
if b >= chosen.numBlk1 {
cwLen = chosen.dataCW2
}
blk := dataCW[offset : offset+cwLen]
offset += cwLen
dataBlocks[b] = blk
ecBlocks[b] = rsRemainder(blk, chosen.ecPerBlk)
}

var allCW []byte
maxDataLen := chosen.dataCW1
if chosen.dataCW2 > maxDataLen {
maxDataLen = chosen.dataCW2
}
for i := 0; i < maxDataLen; i++ {
for b := 0; b < numBlocks; b++ {
if i < len(dataBlocks[b]) {
allCW = append(allCW, dataBlocks[b][i])
}
}
}
for i := 0; i < chosen.ecPerBlk; i++ {
for b := 0; b < numBlocks; b++ {
allCW = append(allCW, ecBlocks[b][i])
}
}

sz := chosen.ver*4 + 17
m := &qrMatrix{
size:    sz,
modules: make([][]bool, sz),
isFunc:  make([][]bool, sz),
}
for r := 0; r < sz; r++ {
m.modules[r] = make([]bool, sz)
m.isFunc[r] = make([]bool, sz)
}

setFunc := func(r, c int, dark bool) {
if r >= 0 && c >= 0 && r < sz && c < sz {
m.modules[r][c] = dark
m.isFunc[r][c] = true
}
}

drawFinder := func(r0, c0 int) {
for dr := -1; dr <= 7; dr++ {
for dc := -1; dc <= 7; dc++ {
dark := (dr >= 0 && dr <= 6 && (dc == 0 || dc == 6)) ||
(dc >= 0 && dc <= 6 && (dr == 0 || dr == 6)) ||
(dr >= 2 && dr <= 4 && dc >= 2 && dc <= 4)
setFunc(r0+dr, c0+dc, dark)
}
}
}
drawFinder(0, 0)
drawFinder(0, sz-7)
drawFinder(sz-7, 0)

// 對齊圖形 (Alignment Patterns)
ac := alignCoords[chosen.ver]
for _, r := range ac {
for _, c := range ac {
if m.isFunc[r][c] {
continue
}
for dr := -2; dr <= 2; dr++ {
for dc := -2; dc <= 2; dc++ {
dark := dr == -2 || dr == 2 || dc == -2 || dc == 2 || (dr == 0 && dc == 0)
setFunc(r+dr, c+dc, dark)
}
}
}
}

// 時序圖形 (Timing Patterns)
for i := 8; i < sz-8; i++ {
if !m.isFunc[6][i] {
setFunc(6, i, i%2 == 0)
}
if !m.isFunc[i][6] {
setFunc(i, 6, i%2 == 0)
}
}

// 保留格式與版本資訊區
for i := 0; i <= 8; i++ {
if !m.isFunc[8][i] {
setFunc(8, i, false)
}
if !m.isFunc[i][8] {
setFunc(i, 8, false)
}
}
for i := 0; i < 8; i++ {
setFunc(8, sz-1-i, false)
setFunc(sz-1-i, 8, false)
}
setFunc(sz-8, 8, true)

if chosen.ver >= 7 {
vBits := chosen.ver << 12
rem := vBits
for i := 17; i >= 12; i-- {
if (rem>>i)&1 != 0 {
rem ^= 0x1f25 << (i - 12)
}
}
vBits |= rem
for i := 0; i < 18; i++ {
dark := (vBits>>i)&1 != 0
setFunc(sz-11+i%3, i/3, dark)
setFunc(i/3, sz-11+i%3, dark)
}
}

// 蛇形填入資料位元並套用 Mask 0 ((r+c)%2 == 0)
bitIdx := 0
totalBits := len(allCW) * 8
upward := true
for right := sz - 1; right >= 1; right -= 2 {
if right == 6 {
right = 5
}
for step := 0; step < sz; step++ {
r := sz - 1 - step
if !upward {
r = step
}
for dx := 0; dx < 2; dx++ {
c := right - dx
if m.isFunc[r][c] {
continue
}
dark := false
if bitIdx < totalBits {
dark = ((allCW[bitIdx/8] >> (7 - bitIdx%8)) & 1) != 0
bitIdx++
}
if (r+c)%2 == 0 {
dark = !dark
}
m.modules[r][c] = dark
}
}
upward = !upward
}

// 寫入格式資訊 (ECC + Mask 0)
eccBits := map[string]int{"M": 0, "L": 1, "H": 2, "Q": 3}[ecc]
fmtData := (eccBits << 3) | 0
rem := fmtData << 10
for i := 14; i >= 10; i-- {
if (rem>>i)&1 != 0 {
rem ^= 0x537 << (i - 10)
}
}
fmtWord := ((fmtData << 10) | rem) ^ 0x5412
for i := 0; i < 15; i++ {
dark := (fmtWord>>i)&1 != 0
if i < 6 {
m.modules[8][i] = dark
} else if i < 8 {
m.modules[8][i+1] = dark
} else {
m.modules[8][sz-15+i] = dark
}
if i < 8 {
m.modules[sz-1-i][8] = dark
} else if i < 9 {
m.modules[15-i][8] = dark
} else {
m.modules[14-i][8] = dark
}
}

return m, ecc, nil
}

// ============================================================================
// 合併閉合輪廓追蹤與 SVG / AutoCAD R12 DXF 向量匯出引擎 (移植自附件 HTML)
// ============================================================================

func traceQRContours(m *qrMatrix) [][][2]int {
n := m.size
isDark := func(r, c int) bool {
return r >= 0 && c >= 0 && r < n && c < n && m.modules[r][c]
}

type dirVec [2]int
out := make(map[string][]dirVec)
totalEdges := 0

addEdge := func(x, y, dx, dy int) {
k := fmt.Sprintf("%d,%d", x, y)
out[k] = append(out[k], dirVec{dx, dy})
totalEdges++
}

for r := 0; r < n; r++ {
for c := 0; c < n; c++ {
if !isDark(r, c) {
continue
}
if !isDark(r-1, c) {
addEdge(c, r, 1, 0)
}
if !isDark(r, c+1) {
addEdge(c+1, r, 0, 1)
}
if !isDark(r+1, c) {
addEdge(c+1, r+1, -1, 0)
}
if !isDark(r, c-1) {
addEdge(c, r+1, 0, -1)
}
}
}

takeEdge := func(x, y, pdx, pdy int) (dirVec, bool) {
k := fmt.Sprintf("%d,%d", x, y)
list := out[k]
if len(list) == 0 {
return dirVec{}, false
}
prefs := []dirVec{{-pdy, pdx}, {pdx, pdy}, {pdy, -pdx}}
for _, pref := range prefs {
for j, cand := range list {
if cand == pref {
out[k] = append(list[:j], list[j+1:]...)
return cand, true
}
}
}
chosen := list[0]
out[k] = list[1:]
return chosen, true
}

keys := make([]string, 0, len(out))
for k := range out {
keys = append(keys, k)
}
sort.Slice(keys, func(i, j int) bool {
var x1, y1, x2, y2 int
fmt.Sscanf(keys[i], "%d,%d", &x1, &y1)
fmt.Sscanf(keys[j], "%d,%d", &x2, &y2)
if y1 != y2 {
return y1 < y2
}
return x1 < x2
})

var loops [][][2]int
for _, k := range keys {
for len(out[k]) > 0 {
var sx, sy int
fmt.Sscanf(k, "%d,%d", &sx, &sy)
dir := out[k][0]
out[k] = out[k][1:]

pts := [][2]int{{sx, sy}}
x, y := sx+dir[0], sy+dir[1]
guard := 0
for !(x == sx && y == sy) {
nd, ok := takeEdge(x, y, dir[0], dir[1])
guard++
if !ok || guard > totalEdges+8 {
break
}
if nd[0] != dir[0] || nd[1] != dir[1] {
pts = append(pts, [2]int{x, y})
}
dir = nd
x += dir[0]
y += dir[1]
}
loops = append(loops, splitSimplePolygon(pts)...)
}
}
return loops
}

func splitSimplePolygon(pts [][2]int) [][][2]int {
var result [][][2]int
var stack [][2]int
pos := make(map[string]int)

for _, pt := range pts {
k := fmt.Sprintf("%d,%d", pt[0], pt[1])
if at, exists := pos[k]; exists {
sub := make([][2]int, len(stack[at:]))
copy(sub, stack[at:])
for j := at + 1; j < len(stack); j++ {
delete(pos, fmt.Sprintf("%d,%d", stack[j][0], stack[j][1]))
}
stack = stack[:at+1]
result = append(result, sub)
} else {
pos[k] = len(stack)
stack = append(stack, pt)
}
}
if len(stack) >= 3 {
result = append(result, stack)
}
return result
}

func GenerateQRVector(req QRVectorRequest) QRVectorResponse {
text := strings.TrimSpace(req.Text)
if text == "" {
return QRVectorResponse{OK: false, Error: "請輸入要編碼的文字或網址 (text 不可為空)"}
}
if req.SizeMM <= 0 {
req.SizeMM = 20.0
}
if req.SizeMM > 95.0 {
req.SizeMM = 95.0
}
quiet := req.QuietZone
if quiet < 0 {
quiet = 4
}
if quiet > 10 {
quiet = 10
}

mat, eccLabel, err := encodePureQR(text, req.ECC)
if err != nil {
return QRVectorResponse{OK: false, Error: err.Error()}
}

n := mat.size
totalModules := n + quiet*2
var moduleMM float64
switch strings.ToLower(strings.TrimSpace(req.SizeBasis)) {
case "module":
moduleMM = req.SizeMM
case "code":
moduleMM = req.SizeMM / float64(n)
default:
moduleMM = req.SizeMM / float64(totalModules)
}

overallMM := moduleMM * float64(totalModules)
codeMM := moduleMM * float64(n)
loops := traceQRContours(mat)

var svgPaths []string
var contoursMM [][][2]float64
for _, pts := range loops {
var seg strings.Builder
var polyMM [][2]float64
for j, pt := range pts {
px := float64(pt[0]+quiet) * moduleMM
py := float64(pt[1]+quiet) * moduleMM
if j == 0 {
seg.WriteString("M" + fmtFloat(px) + "," + fmtFloat(py) + " ")
} else {
seg.WriteString("L" + fmtFloat(px) + "," + fmtFloat(py) + " ")
}
polyMM = append(polyMM, [2]float64{px, py})
}
seg.WriteString("Z")
svgPaths = append(svgPaths, seg.String())
contoursMM = append(contoursMM, polyMM)
}

var svgBuf strings.Builder
svgBuf.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
svgBuf.WriteString(fmt.Sprintf("<svg xmlns=\"http://www.w3.org/2000/svg\" version=\"1.1\" width=\"%smm\" height=\"%smm\" viewBox=\"0 0 %s %s\">\n",
fmtFloat(overallMM), fmtFloat(overallMM), fmtFloat(overallMM), fmtFloat(overallMM)))
svgBuf.WriteString("<path fill=\"#000000\" fill-rule=\"evenodd\" stroke=\"none\" d=\"")
svgBuf.WriteString(strings.Join(svgPaths, "\n"))
svgBuf.WriteString("\"/>\n</svg>\n")

dxfLines := []string{
"0", "SECTION", "2", "HEADER",
"9", "$ACADVER", "1", "AC1009",
"9", "$EXTMIN", "10", "0", "20", "0", "30", "0",
"9", "$EXTMAX", "10", fmtFloat(overallMM), "20", fmtFloat(overallMM), "30", "0",
"0", "ENDSEC",
"0", "SECTION", "2", "ENTITIES",
}
for _, pts := range loops {
dxfLines = append(dxfLines, "0", "POLYLINE", "8", "QR", "66", "1", "10", "0", "20", "0", "30", "0", "70", "1")
for _, pt := range pts {
x := float64(pt[0]+quiet) * moduleMM
y := overallMM - float64(pt[1]+quiet)*moduleMM
dxfLines = append(dxfLines, "0", "VERTEX", "8", "QR", "10", fmtFloat(x), "20", fmtFloat(y), "30", "0")
}
dxfLines = append(dxfLines, "0", "SEQEND", "8", "QR")
}
dxfLines = append(dxfLines, "0", "ENDSEC", "0", "EOF")

return QRVectorResponse{
OK:           true,
ModuleCount:  n,
QuietZone:    quiet,
ECC:          eccLabel,
OverallMM:    math.Round(overallMM*1000) / 1000,
CodeMM:       math.Round(codeMM*1000) / 1000,
ModuleMM:     math.Round(moduleMM*1000) / 1000,
ContourCount: len(loops),
ContoursMM:   contoursMM,
SVG:          svgBuf.String(),
DXF:          strings.Join(dxfLines, "\r\n") + "\r\n",
}
}