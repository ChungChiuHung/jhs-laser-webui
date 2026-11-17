import frida
import sys

target_pid = int(sys.argv[1])
log_file = open(r"G:\CodeHere\jhs-laser-webui\ezcad_f1_dump.txt", "w", encoding="utf-8")

def on_message(message, data):
    if message["type"] == "send":
        text = message["payload"]
        print(text)
        log_file.write(text + "\n")
        log_file.flush()

JS_CODE = """
const mod = Process.findModuleByName("LMCMIO.dll");
send("[*] 成功鎖定 LMCMIO.dll @ " + mod.base);

let lastBufSig = "";
let lastPortSig = "";

mod.enumerateExports().forEach(exp => {
    // 排除高頻狀態查詢函式
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
                    if (cmd === 0x8002) {
                        lines.push("  [Slot " + slot + "] 0x8002 (EndOfList)");
                        break;
                    }
                    lines.push("  [Slot " + slot + "] cmd=0x" + cmd.toString(16).padStart(4,"0") +
                               " | p1=" + p1 + "(0x" + p1.toString(16) + ")" +
                               " | p2=" + p2 + "(0x" + p2.toString(16) + ")" +
                               " | p3=" + p3 + " | p4=" + p4 + "(0x" + p4.toString(16) + ") | p5=" + p5);
                }
                const sig = lines.join("\\n");
                if (sig !== lastBufSig) {
                    lastBufSig = sig;
                    send("\\n================ [MIO_WriteCmdBuf 清單變化] ================\\n" + sig + "\\n============================================================");
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
                // 過濾掉 ReadPort(0x25), GetListStatus(0x0a), GetPosXY(0x0c)
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
send("[*] 監控器已成功掛載！請現在切換到 EzCad2 視窗，按下 [紅光 (F1)] 讓它畫 2 秒正方形，再按 Esc 停止...");
"""

session = frida.attach(target_pid)
script = session.create_script(JS_CODE)
script.on("message", on_message)
script.load()
sys.stdin.read()