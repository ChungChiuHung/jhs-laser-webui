import frida
import sys

JS_CODE = """
const mod = Process.findModuleByName("LMCMIO.dll");
if (!mod) {
    console.log("[!] 尚未載入 LMCMIO.dll");
} else {
    console.log("[*] 成功鎖定 LMCMIO.dll @ " + mod.base);
    const exports = mod.enumerateExports();
    
    exports.forEach(exp => {
        // 攔截所有關鍵控制函式 (排除高頻輪詢的 GetState / ReadPort 避免洗版)
        if (exp.name.indexOf("MIO_GetState") !== -1 || 
            exp.name.indexOf("MIO_ReadPort") !== -1 || 
            exp.name.indexOf("MIO_GetListStatus") !== -1 ||
            exp.name.indexOf("MIO_GetPositionXY") !== -1) {
            return;
        }

        Interceptor.attach(exp.address, {
            onEnter: function(args) {
                // MSVC x86 cdecl: 若回傳 tagResult 結構體，args[0] 為隱藏的 return struct 指標！
                const returnsStruct = exp.name.indexOf("tagResult") !== -1;
                const baseIdx = returnsStruct ? 1 : 0;

                if (exp.name.indexOf("MIO_WriteCmdBuf") !== -1) {
                    const cardId = args[baseIdx].toInt32() & 0xFF;
                    const bufPtr = args[baseIdx + 1];
                    // 印出 tagLmcCmd 前 32 個 uint16 (64 bytes) 指令內容
                    const words = [];
                    for (let i = 0; i < 32; i++) {
                        const w = bufPtr.add(i * 2).readU16();
                        words.push("0x" + w.toString(16).padStart(4, "0"));
                    }
                    console.log("\\n[WriteCmdBuf] card=" + cardId + " | 前32組指令碼(uint16):\\n  " + words.join(" "));
                } 
                else if (exp.name.indexOf("MIO_Cmd") !== -1) {
                    const card = args[baseIdx].toInt32() & 0xFF;
                    const cmd  = args[baseIdx+1].toInt32() & 0xFFFF;
                    const p1   = args[baseIdx+2].toInt32() & 0xFFFF;
                    const p2   = args[baseIdx+3].toInt32() & 0xFFFF;
                    const p3   = args[baseIdx+4].toInt32() & 0xFFFF;
                    const p4   = args[baseIdx+5].toInt32() & 0xFFFF;
                    console.log("[MIO_Cmd] cmd=0x" + cmd.toString(16).padStart(4,"0") + 
                                " p1=" + p1 + "(0x" + p1.toString(16) + ")" +
                                " p2=" + p2 + "(0x" + p2.toString(16) + ")" +
                                " p3=" + p3 + " p4=" + p4);
                }
                else if (exp.name.indexOf("MIO_GotoXY") !== -1) {
                    const card = args[baseIdx].toInt32() & 0xFF;
                    const x    = args[baseIdx+1].toInt32() & 0xFFFF;
                    const y    = args[baseIdx+2].toInt32() & 0xFFFF;
                    console.log("[MIO_GotoXY] card=" + card + " X=" + x + " Y=" + y);
                }
                else if (exp.name.indexOf("MIO_WritePort") !== -1) {
                    const card = args[baseIdx].toInt32() & 0xFF;
                    const v1   = args[baseIdx+1].toInt32() & 0xFFFF;
                    const v2   = args[baseIdx+2].toInt32() & 0xFFFF;
                    console.log("[MIO_WritePort] card=" + card + " arg1=0x" + v1.toString(16) + " arg2=0x" + v2.toString(16));
                }
                else {
                    console.log("[Call] " + exp.name);
                }
            }
        });
    });
    console.log("[*] 攔截器已就緒！請在 EzCad2 視窗中按下 F1 (紅光預覽) 或移動物件...");
}
"""

def main():
    try:
        session = frida.attach("EzCad2.exe")
    except Exception as e:
        print(f"[!] 無法附著到 EzCad2.exe，請先開啟原廠 EzCad2.exe 軟體！錯誤: {e}")
        sys.exit(1)

    script = session.create_script(JS_CODE)
    script.load()
    sys.stdin.read()

if __name__ == "__main__":
    main()