import com.cabinet.chess.gamelogic.Board;
import com.cabinet.chess.gamelogic.Move;
import com.cabinet.chess.gamelogic.Piece;
import com.cabinet.chess.gamelogic.Position;
import com.cabinet.chess.gamelogic.Rule;
import com.cabinet.chess.gamelogic.Zobrist;

import java.io.FileWriter;
import java.io.IOException;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.util.ArrayList;
import java.util.List;
import java.util.Random;

/**
 * 桔库查询向量生成器（Java 端权威实现，参照 作者先前项目原版逻辑）。
 *
 * 用途：对固定局面集计算 Zobrist 哈希并查询桔库，输出 JSON 向量到
 * cabinet/testdata/，供 Go 移植版（internal/book）做双端一致性比对。
 *
 * 权威依据（与作者先前项目逐字一致）：
 * - 哈希：Zobrist.getZobristFromBoard（gamelogic/Zobrist.java，逐子异或 + 红方 XOR player）
 * - 查询 SQL（openbook/BHOpenBook.java:27-32）：
 *   select * from bhobk where vvalid = 1 and vkey = %d order by vscore desc, vwin desc limit 5
 * - 着法解码：Zobrist.getMoveFromVmove（高 8 位起点、低 8 位终点，输出"终点+起点"）
 * - vwin 按 double 读取（BHOpenBook.java:41，尽管建表声明 INTEGER）
 *
 * 编译运行（见同目录 README.md）。注意：BHOpenBook/BHDatabase 依赖 Android
 * SQLiteAssetHelper，本工具不复用它们，改用 sqlite-jdbc 以同一 SQL 查询。
 */
public class Main {

    /** 覆盖用例清单：静态 FEN + 推演序列（见 buildCases）。 */
    private record Vec(String caseName, String fen, boolean redGo, long hash, boolean hit,
                       String bestMove, List<MoveRec> moves) {
    }

    private record MoveRec(String move, int score, double winRate, int draw, int lost) {
    }

    private static final String START_FEN = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1";

    public static void main(String[] args) throws Exception {
        if (args.length < 3) {
            System.err.println("用法: java Main <桔库.obk 路径> <锚点向量输出 JSON> <随机向量输出 JSON>");
            System.exit(1);
        }
        String bookPath = args[0];
        String anchorOut = args[1];
        String randomOut = args[2];

        List<Case> anchorCases = buildCases();
        List<Vec> anchorVectors = new ArrayList<>();
        List<Case> randomCases = randomGameCases();
        List<Vec> randomVectors = new ArrayList<>();
        try (Connection db = DriverManager.getConnection("jdbc:sqlite:" + bookPath)) {
            for (Case c : anchorCases) {
                anchorVectors.add(query(db, c));
            }
            for (Case c : randomCases) {
                randomVectors.add(query(db, c));
            }
        }

        writeJson(anchorOut, bookPath, anchorVectors, "锚点语义用例（手挑，14 条）");
        writeJson(randomOut, bookPath, randomVectors, "随机合法对局推演空间覆盖（20 局×≤60 步，种子固定可复现）");

        long aHits = anchorVectors.stream().filter(Vec::hit).count();
        long rHits = randomVectors.stream().filter(Vec::hit).count();
        System.out.printf("锚点向量 %d 条（命中 %d）；随机向量 %d 条（命中 %d）%n",
                anchorVectors.size(), aHits, randomVectors.size(), rHits);
    }

    // ─────────────── 随机合法对局推演 ───────────────

    /** 固定种子：向量集确定可复现（换种子即换覆盖空间）。 */
    private static final long RANDOM_SEED = 20260910L;
    private static final int RANDOM_GAMES = 20;
    private static final int RANDOM_PLIES = 60;

    /**
     * 20 局随机合法对局：从起始局面每步均匀随机选一个真合法着法（走后己帅不危险、
     * 不形成两将照面；被将军时天然只剩应将着法），≤60 步，含吃子/残缺士相/兵压底等
     * 起始线覆盖不到的棋子配置。提前无子可走（困毙/将死）则该局提前结束。
     */
    private static List<Case> randomGameCases() {
        List<Case> list = new ArrayList<>();
        Random rnd = new Random(RANDOM_SEED);
        int total = 0;
        for (int g = 0; g < RANDOM_GAMES; g++) {
            Board b = new Board();
            if (!b.restoreFromFEN(START_FEN)) {
                throw new IllegalStateException("起始 FEN 解析失败");
            }
            list.add(new Case(String.format("g%02d_p%03d", g, 0), b.toFENString()));
            total++;
            for (int ply = 1; ply <= RANDOM_PLIES; ply++) {
                List<Move> legal = legalMoves(b);
                if (legal.isEmpty()) {
                    break; // 困毙/将死：该局提前结束
                }
                Move m = legal.get(rnd.nextInt(legal.size()));
                b.doMove(m);
                list.add(new Case(String.format("g%02d_p%03d", g, ply), b.toFENString()));
                total++;
            }
        }
        System.out.println("随机对局推演: " + RANDOM_GAMES + " 局，共 " + total + " 局面");
        return list;
    }

    /** 枚举当前行棋方的全部真合法着法（走后己帅不危险且不两将照面）。 */
    private static List<Move> legalMoves(Board b) {
        boolean redGo = b.bRedGo;
        int myKing = redGo ? Piece.WSHUAI : Piece.BJIANG;
        int opKing = redGo ? Piece.BJIANG : Piece.WSHUAI;
        List<Move> out = new ArrayList<>();
        for (int y = 0; y < 10; y++) {
            for (int x = 0; x < 9; x++) {
                int p = b.getPieceByPosition(x, y);
                if (p == Piece.EMPTY || !isMine(p, redGo)) {
                    continue;
                }
                for (Position to : Rule.PossibleToPositions(p, x, y, b)) {
                    int target = b.getPieceByPosition(to);
                    if (target == opKing) {
                        continue; // 不吃将（上一手已过滤送将，此为防御）
                    }
                    // 模拟走子
                    b.setPieceByPosition(to, p);
                    b.setPieceByPosition(x, y, Piece.EMPTY);
                    boolean bad = false;
                    Position kp = Rule.findJiangShuaiPos(myKing, b);
                    if (kp == null || Rule.isJiangShuaiInDanger(myKing, kp, b)) {
                        bad = true; // 走后己帅被攻击（含应将过滤：被将军时只剩解将着法）
                    } else {
                        Position op = Rule.findJiangShuaiPos(opKing, b);
                        if (kp.x == op.x) {
                            boolean blocked = false;
                            for (int yy = Math.min(kp.y, op.y) + 1; yy < Math.max(kp.y, op.y); yy++) {
                                if (b.getPieceByPosition(kp.x, yy) != Piece.EMPTY) {
                                    blocked = true;
                                    break;
                                }
                            }
                            if (!blocked) {
                                bad = true; // 两将照面
                            }
                        }
                    }
                    // 撤销模拟
                    b.setPieceByPosition(x, y, p);
                    b.setPieceByPosition(to, target);
                    if (!bad) {
                        out.add(new Move(new Position(x, y), to));
                    }
                }
            }
        }
        return out;
    }

    private static boolean isMine(int piece, boolean redGo) {
        return redGo ? piece >= Piece.WSHUAI && piece <= Piece.WBING
                : piece >= Piece.BJIANG && piece <= Piece.BZU;
    }

    /** 向量文件写出（锚点与随机集共用，结构一致）。 */
    private static void writeJson(String outPath, String bookPath, List<Vec> vectors, String purpose)
            throws IOException {
        try (FileWriter w = new FileWriter(outPath)) {
            w.write("{\n");
            w.write("  \"generated_by\": \"tools/vector-gen (作者先前项目原版 Zobrist/Board + sqlite-jdbc，SQL 逐字照抄 BHOpenBook.java)\",\n");
            w.write("  \"purpose\": \"" + purpose + "\",\n");
            w.write("  \"book\": \"" + bookPath.replace("\\", "/") + "\",\n");
            w.write("  \"vectors\": [\n");
            for (int i = 0; i < vectors.size(); i++) {
                Vec v = vectors.get(i);
                w.write("    {\n");
                w.write("      \"case\": \"" + v.caseName() + "\",\n");
                w.write("      \"fen\": \"" + v.fen() + "\",\n");
                w.write("      \"red_go\": " + v.redGo() + ",\n");
                w.write("      \"hash\": " + v.hash() + ",\n");
                w.write("      \"hit\": " + v.hit() + ",\n");
                w.write("      \"best_move\": \"" + (v.bestMove() == null ? "" : v.bestMove()) + "\",\n");
                w.write("      \"moves\": [\n");
                for (int j = 0; j < v.moves().size(); j++) {
                    MoveRec m = v.moves().get(j);
                    w.write("        {\"move\": \"" + m.move() + "\", \"score\": " + m.score()
                            + ", \"win_rate\": " + m.winRate() + ", \"draw\": " + m.draw()
                            + ", \"lost\": " + m.lost() + "}"
                            + (j + 1 < v.moves().size() ? "," : "") + "\n");
                }
                w.write("      ]\n");
                w.write("    }" + (i + 1 < vectors.size() ? "," : "") + "\n");
            }
            w.write("  ]\n}\n");
        }
        long hits = vectors.stream().filter(Vec::hit).count();
        System.out.println("向量写出: " + vectors.size() + " 条（命中 " + hits + "）→ " + outPath);
    }

    /** 覆盖用例：红先/黑先、镜像局面、空查询（未命中）、真实开局推演序列。 */
    private static List<Case> buildCases() {
        List<Case> list = new ArrayList<>();
        // 1. 起始局面：红先
        list.add(new Case("startpos_red", START_FEN));
        // 2. 同盘黑先（验证 zobristPlayer 项）
        list.add(new Case("startpos_black", START_FEN.replace(" w - -", " b - -")));
        // 3. 空查询（未命中）：光帅对光将，库中必有此局面之外
        list.add(new Case("miss_kings_only", "4k4/9/9/9/9/9/9/9/9/4K4 w - - 0 1"));

        // 4. 真实开局推演（中炮对屏风马前段）：每步都出向量，红黑先自然交替
        String[] line = {"h2e2", "h9g7", "h0g2", "b9c7", "i0h0", "a9b9", "b0c2", "c6c5"};
        Board b = new Board();
        if (!b.restoreFromFEN(START_FEN)) {
            throw new IllegalStateException("起始 FEN 解析失败");
        }
        list.add(new Case("line_" + 0 + "_" + (b.bRedGo ? "w" : "b"), b.toFENString()));
        for (int i = 0; i < line.length; i++) {
            Move m = new Move(b);
            if (!m.fromUCCIString(line[i])) {
                throw new IllegalStateException("着法解析失败: " + line[i]);
            }
            b.doMove(m);
            list.add(new Case("line_" + (i + 1) + "_" + (b.bRedGo ? "w" : "b"), b.toFENString()));
        }

        // 5. 镜像局面：把"马二进三后"的局面左右翻转（非对称局面镜像，两侧先手各一）
        Board mirrored = mirror(b);
        mirrored.bRedGo = true;
        list.add(new Case("mirror_red", mirrored.toFENString()));
        mirrored.bRedGo = false;
        list.add(new Case("mirror_black", mirrored.toFENString()));
        return list;
    }

    /** 左右镜像：列 x → 8-x。 */
    private static Board mirror(Board src) {
        Board dst = new Board();
        dst.clear();
        for (int y = 0; y < 10; y++) {
            for (int x = 0; x < 9; x++) {
                dst.setPieceByPosition(8 - x, y, src.getPieceByPosition(x, y));
            }
        }
        return dst;
    }

    /** 对单个局面计算哈希并查库（SQL 逐字照抄 BHOpenBook.get）。 */
    private static Vec query(Connection db, Case c) throws Exception {
        Board b = new Board();
        if (!b.restoreFromFEN(c.fen)) {
            throw new IllegalStateException("FEN 解析失败: " + c.fen);
        }
        boolean redGo = b.bRedGo;
        long hash = b.getZobrist(redGo);

        List<MoveRec> moves = new ArrayList<>();
        String best = null;
        boolean hit = false;
        // 逐字对齐 BHOpenBook.java:27-32（含排序与 limit 5）
        String sql = "select * from bhobk where vvalid = 1 and vkey = ? order by vscore desc, vwin desc limit 5";
        try (PreparedStatement ps = db.prepareStatement(sql)) {
            ps.setLong(1, hash);
            try (ResultSet rs = ps.executeQuery()) {
                while (rs.next()) {
                    int vmove = rs.getInt("vmove");
                    String move = Zobrist.getMoveFromVmove(vmove);
                    if (best == null) {
                        best = move;
                    }
                    hit = true;
                    moves.add(new MoveRec(move,
                            rs.getInt("vscore"),
                            rs.getDouble("vwin"), // BHOpenBook 按 double 读（schema 声明 INTEGER 亦然）
                            rs.getInt("vdraw"),
                            rs.getInt("vlost")));
                }
            }
        }
        System.out.printf("case=%-16s redGo=%-5s hash=%d hit=%s best=%s%n",
                c.name, redGo, hash, hit, best);
        return new Vec(c.name, c.fen, redGo, hash, hit, best, moves);
    }

    private record Case(String name, String fen) {
    }
}
