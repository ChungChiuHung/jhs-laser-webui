import frida
import sys

target_pid = int(sys.argv[1])

JS_CODE = """
const mod = Process.findModuleByName("LMCMIO.dll");
console.log("[*] 成功鎖定 LMCMIO.dll @ " + mod.base);

mod.enumerateExports().forEach(exp => {
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
                console.log("\\n================ [MIO_WriteCmdBuf 完整指令清單] ================");
                for (let slot = 0; slot < 256; slot++) {
                    const cmd = bufPtr.add(slot * 12).readU16();
                    if (cmd === 0x8002) {
                        console.log("  [Slot " + slot + "] 0x8002 (EndOfList - 後續省略)");
                        break;
                    }
                    const p1 = bufPtr.add(slot * 12 + 2).readU16();
                    const p2 = bufPtr.add(slot * 12 + 4).readU16();
                    const p3 = bufPtr.add(slot * 12 + 6).readU16();
                    const p4 = bufPtr.add(slot * 12 + 8).readU16();
                    const p5 = bufPtr.add(slot * 12 + 10).readU16();
                    console.log("  [Slot " + slot + "] cmd=0x" + cmd.toString(16).padStart(4,"0") +
                                " | p1=" + p1 + "(0x" + p1.toString(16) + ")" +
                                " | p2=" + p2 + "(0x" + p2.toString(16) + ")" +
                                " | p3=" + p3 + " | p4=" + p4 + "(0x" + p4.toString(16) + ") | p5=" + p5);
                }
                console.log("================================================================");
            }
            else if (exp.name.indexOf("MIO_WritePort") !== -1) {
                const v1 = args[baseIdx+1].toInt32() & 0xFFFF;
                const v2 = args[baseIdx+2].toInt32() & 0xFFFF;
                console.log("[MIO_WritePort] arg1=0x" + v1.toString(16) + " arg2=0x" + v2.toString(16));
            }
            else if (exp.name.indexOf("MIO_GotoXY") !== -1) {
                const x = args[baseIdx+1].toInt32() & 0xFFFF;
                const y = args[baseIdx+2].toInt32() & 0xFFFF;
                console.log("[MIO_GotoXY] X=" + x + " Y=" + y);
            }
            else if (exp.name.indexOf("MIO_Cmd") !== -1) {
                const cmd = args[baseIdx+1].toInt32() & 0xFFFF;
                if (cmd !== 0x0025 && cmd !== 0x000a && cmd !== 0x000c) {
                    const p1 = args[baseIdx+2].toInt32() & 0xFFFF;
                    const p2 = args[baseIdx+3].toInt32() & 0xFFFF;
                    console.log("[MIO_Cmd] cmd=0x" + cmd.toString(16).padStart(4,"0") + " p1=0x" + p1.toString(16) + " p2=0x" + p2.toString(16));
                }
            }
            else {
                console.log("[Call] " + exp.name);
            }
        }
    });
});
console.log("[*] 攔截器已就緒！請在剛才彈出的 EzCad 視窗中畫一個矩形，然後按一下 F1 (紅光預覽) 跑 1 秒再按 Esc 停止...");
"""

session = frida.attach(target_pid)
script = session.create_script(JS_CODE)
script.load()
sys.stdin.read()