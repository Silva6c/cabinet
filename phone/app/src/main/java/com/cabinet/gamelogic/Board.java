package com.cabinet.gamelogic;

import android.util.Log;

import java.io.Serializable;

/**
 * Created by zfdang on 2016-4-17.
 * 棋盘数据（9×10 int 数组），本质是 FEN 局面的内存表示：restoreFromFEN 解析 FEN，
 * piece[y][x] 存 Piece 常量（0=空）。坐标系：x=0..8 自左向右、y=0..9 自上而下
 * （y=0 黑方底线、y=9 红方底线）。取子一律走 getPieceByPosition——数组是 [y][x]
 * 行主序，直接下标极易把 x/y 写反。
 */
public class Board implements Serializable {
    private static final long serialVersionUID = 2194052829642412444L;

    static public int BOARD_PIECE_WIDTH = 9;
    static public int BOARD_PIECE_HEIGHT = 10;

    // 勿直接访问 piece 数组：[y][x] 行主序，直接下标极易把 x/y 写反，取子走 getPieceByPosition
    private int[][] piece = new int[][]{
            {Piece.BJU, Piece.BMA, Piece.BXIANG, Piece.BSHI, Piece.BJIANG, Piece.BSHI, Piece.BXIANG, Piece.BMA, Piece.BJU},
            {0, 0, 0, 0, 0, 0, 0, 0, 0},
            {0, Piece.BPAO, 0, 0, 0, 0, 0, Piece.BPAO, 0},
            {Piece.BZU, 0, Piece.BZU, 0, Piece.BZU, 0, Piece.BZU, 0, Piece.BZU},
            {0, 0, 0, 0, 0, 0, 0, 0, 0},

            {0, 0, 0, 0, 0, 0, 0, 0, 0},
            {Piece.WBING, 0, Piece.WBING, 0, Piece.WBING, 0, Piece.WBING, 0, Piece.WBING},
            {0, Piece.WPAO, 0, 0, 0, 0, 0, Piece.WPAO, 0},
            {0, 0, 0, 0, 0, 0, 0, 0, 0},
            {Piece.WJU, Piece.WMA, Piece.WXIANG, Piece.WSHI, Piece.WSHUAI, Piece.WSHI, Piece.WXIANG, Piece.WMA, Piece.WJU},
    };

    public static boolean isValidPosition(Position pos) {
        if(pos == null) return false;
        return pos.x >= 0 && pos.x < BOARD_PIECE_WIDTH && pos.y >= 0 && pos.y < BOARD_PIECE_HEIGHT;
    }

    public Board() {
    }

    public Board(Board b) {
        for (int i = 0; i < this.piece.length; i++) {
            this.piece[i] = b.piece[i].clone();
        }
    }

    public int getPieceByPosition(Position pos) {
        if(isValidPosition(pos)){
            return piece[pos.y][pos.x];
        } else {
            return -1;
        }
    }

    public int getPieceByPosition(int x, int y) {
        if (x >= 0 && x < BOARD_PIECE_WIDTH && y >= 0 && y < BOARD_PIECE_HEIGHT) {
            return piece[y][x];
        } else {
            return -1;
        }
    }

    /**
     * 解析 FEN 逐格覆盖整个棋盘（合法 FEN 恰好写满 90 格，复用实例无需先清盘）。
     * 防御有限：仅校验段数/行棋方/回合数三处，返回 false 时棋盘内容不变；但盘面段
     * 含未知棋子字符会因查表拆箱抛 NPE，格子数不足则残留旧子且仍返回 true——
     * 调用方须保证 FEN 来自可信端（协议/引擎），勿把用户输入直接灌入。
     */
    public boolean restoreFromFEN(String fenString) {
        if(fenString == null) return false;
        fenString = fenString.trim();

        // 容忍只写到行棋方的短 FEN（"…盘面 w/b"）：补齐后缀凑满 6 段再解析
        if(fenString.endsWith("w") || fenString.endsWith("b")) {
            fenString += " - - 0 1";
        }

        // parse fen string
        String[] parts = fenString.split(" ");
        if (parts.length != 6) {
            Log.e("Board", "Failed to parse FEN string: " + fenString);
            return false;
        }

        // parse isRedGo（side 段合法性兼作 FEN 格式校验）
        String side = parts[1].toLowerCase();
        if(!side.equals("w") && !side.equals("r") && !side.equals("b")) {
            Log.e("Board", "Failed to parse side from FEN string: " + fenString);
            return false;
        }

        // parse rounds（rounds 段数值合法性兼作 FEN 格式校验）
        try {
            Integer.parseInt(parts[5]);
        } catch (NumberFormatException e) {
            Log.e("Board", "Failed to parse rounds from FEN string: " + fenString);
            return false;
        }

        // parse fen string
        String fen = parts[0];
        int x = 0;
        int y = 0;
        for (int i = 0; i < fen.length(); i++) {
            char c = fen.charAt(i);
            if (c == '/') {
                // next row
                x = 0;
                y++;
            } else if (c >= '0' && c <= '9') {
                for (int k = 0; k < c - '0'; k++) {
                    piece[y][x] = 0;
                    x++;
                }
            } else {
                int value = Piece.pieceValueMap.get(c);
                piece[y][x] = value;
                x++;
            }
        }
        return true;
    }
}
