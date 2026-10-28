package com.cabinet.gamelogic;

import java.io.Serializable;

public class Position implements Serializable {
    private static final long serialVersionUID = 1L;

    public int x;
    public int y;

    public Position(int x, int y) {
        this.x = x;
        this.y = y;
    }
}
