import frida
import sys

target_pid = int(sys.argv[1])
dump_path = r"G:\CodeHere\jhs-laser-webui\ezcad_f1_latest.txt"
log_file = open(dump_path, "w", encoding="utf-8")

def on_message(message, data):
    if message["type"] == "send":
        text = message["payload"]
        print(text)
        log_file.write(text + "\n")
        log_file.flush()

JS_CODE = """
const mod = Process.findModuleByName("LMCMIO.dll");
send("[*] 成功鎖定 LMCMIO.dll @ " + mod.base);

const CMD_NAMES = {
    0x8001: "listJumpTo (空移跳轉)",
    0x8002: "listEndofList (清單結束)",
    0x8004: "listDelayTime (延時)",
    0x8005: "listMarkTo (雕刻直線)",
    0x8006: "listJumpSpeed (空移速度)",
    0x8007: "listLaserOnDelay (開光延時)",
    0x8008: "listLaserOffDelay (關光延時)",
    0x800c: "listMarkSpeed (雕刻速度)",
    0x800d: "listJumpDelay (跳轉延時)",
    0x800f: "listPolygonDelay (轉角延時)",
    0x8011: "listWritePort (清單IO輸出)",
    0x8051: "ReadyMark (準備標刻)"
};

let bufCount = 0;
let lastBufSig = "";
let lastPortSig = "";

mod.enumerateExports().forEach(exp => {
    // 排除背景高頻輪詢函式，保持輸出乾淨
    if (exp.name.indexOf("MIO_GetState") !== -1 || 
        exp.name.indexOf("MIO_ReadPort") !== -1 || 
        exp.name.indexOf("MIO_GetListStatus") !== -1 ||
        exp.name.indexOf("MIO_GetPositionXY") !== -1) {
        return;
    }

    Interceptor.attach(exp.address, {
        onEnter: function(args) {
            const returnsStruct = exp.name.indexOf("tagResult") !== -1;
            const baseIdx = returnsStruct ? 1 : 0;

            if (exp.name.indexOf("MIO_WriteCmdBuf") !== -1) {
                const bufPtr = args[baseIdx + 1];
                let lines = [];
                for (let slot = 0; slot < 256; slot++) {
                    const cmd = bufPtr.add(slot * 12).readU16();
                    const p1  = bufPtr.add(slot * 12 + 2).readU16();
                    const p2  = bufPtr.add(slot * 12 + 4).readU16();
                    const p3  = bufPtr.add(slot * 12 + 6).readU16();
                    const p4  = bufPtr.add(slot * 12 + 8).readU16();
                    const p5  = bufPtr.add(slot * 12 + 10).readU16();
                    const cname = CMD_NAMES[cmd] || "0x" + cmd.toString(16);

                    if (cmd === 0x8002) {
                        lines.push("  [Slot " + slot + "] 0x8002 (" + cname + " - 後續省略)");
                        break;
                    }
                    lines.push("  [Slot " + slot + "] 0x" + cmd.toString(16).padStart(4,"0") + " [" + cname + "]" +
                               " | p1=" + p1 + "(0x" + p1.toString(16) + ")" +
                               " | p2=" + p2 + "(0x" + p2.toString(16) + ")" +
                               " | p3=" + p3 + " | p4=" + p4 + "(0x" + p4.toString(16) + ") | p5=" + p5);
                }
                const sig = lines.join("\\n");
                if (sig !== lastBufSig) {
                    lastBufSig = sig;
                    bufCount++;
                    send("\\n================ [MIO_WriteCmdBuf #" + bufCount + "] ================\\n" + sig + "\\n================================================================");
                }
            }
            else if (exp.name.indexOf("MIO_WritePort") !== -1) {
                const v1 = args[baseIdx+1].toInt32() & 0xFFFF;
                const v2 = args[baseIdx+2].toInt32() & 0xFFFF;
                const pSig = v1 + "_" + v2;
                if (pSig !== lastPortSig) {
                    lastPortSig = pSig;
                    send("[MIO_WritePort] arg1=0x" + v1.toString(16) + " arg2=0x" + v2.toString(16));
                }
            }
            else if (exp.name.indexOf("MIO_GotoXY") !== -1) {
                const x = args[baseIdx+1].toInt32() & 0xFFFF;
                const y = args[baseIdx+2].toInt32() & 0xFFFF;
                send("[MIO_GotoXY] X=" + x + "(0x" + x.toString(16) + ") Y=" + y + "(0x" + y.toString(16) + ")");
            }
            else if (exp.name.indexOf("MIO_Cmd") !== -1) {
                const cmd = args[baseIdx+1].toInt32() & 0xFFFF;
                if (cmd !== 0x0025 && cmd !== 0x000a && cmd !== 0x000c) {
                    const p1 = args[baseIdx+2].toInt32() & 0xFFFF;
                    const p2 = args[baseIdx+3].toInt32() & 0xFFFF;
                    const p3 = args[baseIdx+4].toInt32() & 0xFFFF;
                    send("[MIO_Cmd] cmd=0x" + cmd.toString(16).padStart(4,"0") + " p1=0x" + p1.toString(16) + " p2=0x" + p2.toString(16) + " p3=0x" + p3.toString(16));
                }
            }
            else {
                send("[Call] " + exp.name);
            }
        }
    });
});
send("[*] F1 紅光預覽監控器已就緒！請切回 EzCad2 按下 [紅光 (F1)]，完成後按 Esc 停止...");
"""

session = frida.attach(target_pid)
script = session.create_script(JS_CODE)
script.on("message", on_message)
script.load()
sys.stdin.read()