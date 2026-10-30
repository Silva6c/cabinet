package com.cabinet.gamelogic;

import java.io.Serializable;

/**
 * 盘面坐标：x=列 0..8，y=行 0..9。Board 内部按 piece[y][x] 存取（y=0 黑方底线、
 * y=9 红方底线）；UCCI 坐标与 (x,y) 的换算见 Move.fromUCCIString。
 */
public class Position implements Serializable {
    private static final long serialVersionUID = 1L;

    public int x;
    public int y;

    public Position(int x, int y) {
        this.x = x;
        this.y = y;
    }
}
