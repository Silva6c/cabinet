package com.cabinet.gamelogic;

import android.util.Log;

import java.io.Serializable;

/**
 * Created by zfdang on 2016-4-17.
 * This class represents the chess board data, basically it's represention of FEN string;
 */
public class Board implements Serializable {
    private static final long serialVersionUID = 2194052829642412444L;

    static public int BOARD_PIECE_WIDTH = 9;
    static public int BOARD_PIECE_HEIGHT = 10;

    // do not access piece value directly, use getPieceByPosition instead
    // it's easy to make mistake with values of (x,y)
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

    public boolean restoreFromFEN(String fenString) {
        if(fenString == null) return false;
        fenString = fenString.trim();

        // append missing parts
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
