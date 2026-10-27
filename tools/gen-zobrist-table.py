#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""从 vector-gen 的 Java Zobrist.java 逐字机械转换生成 internal/book/zobrist_table.go。

用法（项目根目录执行）：python tools/gen-zobrist-table.py
源表改动时重跑即可，勿手改生成的 Go 文件。
Java long 十六进制字面量按 64 位补码转成 Go 的带符号十进制（超 int64 上限的
形如 0xA0CE... 实为负数，Go 常量不接受该写法）。
"""
import re

SRC = 'tools/vector-gen/src/com/cabinet/chess/gamelogic/Zobrist.java'
DST = 'internal/book/zobrist_table.go'

def to_int64(hexlit):
    """Java long 十六进制字面量 → Go int64 十进制（按 64 位补码）。"""
    v = int(hexlit.rstrip('L'), 16)
    if v >= 1 << 63:
        v -= 1 << 64
    return v

src = open(SRC, encoding='utf-8').read()
c90_vals = re.findall(r'0x[0-9a-fA-F]+', re.search(r'c90 = new int\[\] \{(.*?)\};', src, re.S).group(1))
assert len(c90_vals) == 90
player = to_int64(re.search(r'zobristPlayer = (0x[0-9a-fA-F]+L);', src).group(1))
table_vals = [to_int64(v) for v in re.findall(r'0x[0-9a-fA-F]+L', re.search(r'zobristTable = new long\[\] \{(.*?)\};', src, re.S).group(1))]
assert len(table_vals) == 3584

def fmt(vals, per_line):
    return '\n'.join('\t' + ', '.join(str(v) for v in vals[i:i+per_line]) + ','
                     for i in range(0, len(vals), per_line))

san = [f"{f}{r}" for r in "9876543210" for f in "abcdefghi"]
go = f'''package book

// 由上游拷贝树 tools/vector-gen/src 下的 gamelogic/Zobrist.java 逐字机械转换生成
//（tools/gen-zobrist-table.py），请勿手改；重新生成见 tools/gen-zobrist-table.py。
// 数据来源：象棋迪克桔库查询键（Zobrist.java 注释：从华弈2022开局库提取）。
// Java long 十六进制字面量已按 64 位补码转十进制（0xA0CE... 等实为负数）。

// zobristPlayer 红方行棋时额外异或的常量。
const zobristPlayer int64 = {player}

// c90 棋盘索引 x+y*9 → 格子编码（0x33 起）。
var c90 = [90]int{{
{fmt([int(v, 16) for v in c90_vals], 9)}
}}

// san90 格子编码 → 坐标名（vmove 解码用）。
var san90 = [90]string{{
{fmt(['"' + v + '"' for v in san], 9)}
}}

// zobristTable[棋子编号-1][格子编码] 的随机数表（14×256=3584 项）。
var zobristTable = [3584]int64{{
{fmt(table_vals, 4)}
}}
'''
open(DST, 'w', encoding='utf-8').write(go)
print(f"OK: {DST} ({len(table_vals)} entries, player={player})")
