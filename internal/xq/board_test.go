package xq

import (
	"testing"

	"cabinet/internal/book"
)

const startFen = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"

// countFrom 统计起点为 v 的伪合法走子数。
func countFrom(b book.Board, redGo bool, v Vec) int {
	n := 0
	for _, m := range PseudoMoves(b, redGo) {
		if m.From == v {
			n++
		}
	}
	return n
}

// TestPseudoMoves_Start 开局各子着法数（逐子手核，不依赖"开局总数"这类口口
// 相传的数字）。推演坐标统一用 (x,y)：y=0 黑底、y=9 红底，红兵前进 = y 减小。
// 注意：cases 的 name 串（如"帅 e9"）只是显示标签、部分沿用了错位记号，UCCI
// 正名以 rank=9-y 为准（(4,9) 帅 = e0）；断言只依赖 Vec，标签不参与判定。
//   - 兵 5×1（未过河只进不横；前进格 y=5 行全空）
//   - 马 2×2（(1,9)：a/c 两向可跳（共用腿 (1,8) 空）；(3,8) 向被 (2,9) 相蹩腿）
//   - 车 2×2（(0,9)：直进 (0,8)/(0,7) 后被 (0,6) 红兵挡；横被 (1,9) 马挡）
//   - 炮 2×12（(1,7)：横 6 = 左 1 + 右 5（(7,7) 己炮停）；竖下 y=6..3 平移 4，
//     y=2 黑炮作架隔吃 (1,0) 黑马 1；竖上 y=8 平移 1，(1,9) 己马截停）
//   - 帅 1（(4,9)：左右被仕占，可进宫心 (4,8)）
//   - 仕 1（(3,9)：斜进 (4,8) 宫内；(2,8) 出宫被拒）
//   - 相 2+2（(2,9)→(0,7)/(4,7)、(6,9)→(4,7)/(8,7)，均红半场 y≥5）
func TestPseudoMoves_Start(t *testing.T) {
	b, redGo, err := book.ParseFEN(startFen)
	if err != nil {
		t.Fatal(err)
	}
	if !redGo {
		t.Fatal("开局应为红方行棋")
	}
	cases := []struct {
		name string
		v    Vec
		want int
	}{
		{"红兵 a6 进", Vec{0, 6}, 1},
		{"b9 马", Vec{1, 9}, 2},
		{"h9 马", Vec{7, 9}, 2},
		{"a9 车", Vec{0, 9}, 2},
		{"i9 车", Vec{8, 9}, 2},
		{"b2 炮", Vec{1, 7}, 12},
		{"h2 炮", Vec{7, 7}, 12},
		{"帅 e9", Vec{4, 9}, 1},
		{"仕 d9", Vec{3, 9}, 1},
		{"相 c9", Vec{2, 9}, 2},
		{"相 g9", Vec{6, 9}, 2},
	}
	for _, c := range cases {
		if got := countFrom(b, redGo, c.v); got != c.want {
			t.Errorf("%s (%v): got %d want %d", c.name, c.v, got, c.want)
		}
	}
}

// TestPseudoLegal_Boundaries 边界语义：蹩马腿/塞象眼/出宫/照面步不生成。
func TestPseudoLegal_Boundaries(t *testing.T) {
	b, redGo, _ := book.ParseFEN(startFen)
	// (7,9) 马去 (5,8)（h0→f1）：腿 (6,9)=g0 相 → 蹩（t.Error 文案沿用旧记号）
	if PseudoLegal(b, redGo, Move{Vec{7, 9}, Vec{5, 8}}) {
		t.Error("h9 马→f8 应被 g9 相蹩腿拒绝")
	}
	// 仕 (3,9)=d0 出宫（去 (2,8)=c1，x2 越出 x3-5 宫带）拒绝
	if PseudoLegal(b, redGo, Move{Vec{3, 9}, Vec{2, 8}}) {
		t.Error("仕出九宫应拒绝")
	}
	// 塞象眼：e2 相去 c0 的眼 (3,8)=d1 被红兵占 → 拒
	eyeFen := "4k4/9/9/9/9/9/9/4B4/3P5/4K4 w - - 0 1"
	eb, ered, err := book.ParseFEN(eyeFen)
	if err != nil {
		t.Fatal(err)
	}
	if PseudoLegal(eb, ered, Move{Vec{4, 7}, Vec{2, 9}}) {
		t.Error("塞象眼应拒绝")
	}
	if !PseudoLegal(eb, ered, Move{Vec{4, 7}, Vec{6, 9}}) {
		t.Error("另一向象步（眼 f1 空）应合法")
	}
	// 帅照面步不生成：构造对脸局面（合法局面里不会出现，直接调 PseudoLegal 防双保险）
	faceFen := "4k4/9/9/9/9/9/9/9/9/4K4 w - - 0 1"
	fb, fred, _ := book.ParseFEN(faceFen)
	if PseudoLegal(fb, fred, Move{Vec{4, 9}, Vec{4, 0}}) {
		t.Error("帅将照面步不应生成")
	}
}

// TestUcci_RoundTrip UcciOf/ParseUcci 恒等与 rank 翻转正确性。
func TestUcci_RoundTrip(t *testing.T) {
	for _, s := range []string{"h2e2", "a0a9", "i9i0", "b2b0"} {
		m, ok := ParseUcci(s)
		if !ok {
			t.Fatalf("%s 解析失败", s)
		}
		if got := UcciOf(m); got != s {
			t.Errorf("UcciOf(ParseUcci(%q)) = %q", s, got)
		}
	}
	if _, ok := ParseUcci("h2e"); ok {
		t.Error("短串应拒绝")
	}
	if _, ok := ParseUcci("j2e2"); ok {
		t.Error("越界列应拒绝")
	}
}

// TestDiffSingleMove 中炮开局一步的 diff 推导与无效形态排除。
func TestDiffSingleMove(t *testing.T) {
	after := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"
	got, ok := DiffSingleMove(startFen, after)
	if !ok || got != "h2e2" {
		t.Fatalf("应推导出 h2e2: got (%q,%v)", got, ok)
	}
	// 悔棋形态：new 的行棋方与 prev 相同（走子方未翻转）→ 拒
	if _, ok := DiffSingleMove(startFen, "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR w - - 0 1"); ok {
		t.Error("行棋方未翻转的两格差异应拒绝（更新轮次/编辑特征）")
	}
	// 多格变化（手动编辑）→ 拒
	multi := "rnbakab1r/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"
	if _, ok := DiffSingleMove(startFen, multi); ok {
		t.Error("多格变化应拒绝")
	}
	// 三格以上（吃子+走子混合）→ 拒
	if _, ok := DiffSingleMove(startFen, "rnbakabnr/9/1c5c1/p1p1p1p1p/9/4P4/8P/1C2C4/9/RNBAKABNR b - - 0 1"); ok {
		t.Error("非单步形态应拒绝")
	}
}

// TestFenAfter 应用一步得新 FEN（行棋方翻转、盘面段重算、后段保留）。
func TestFenAfter(t *testing.T) {
	got, ok := FenAfter(startFen, "h2e2")
	// h2 炮平中 → 行 7 "1C5C1" 变为 x1 C、x4 C、x7 空：1空 C 2空 C 4空 = "1C2C4"
	want := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"
	if !ok || got != want {
		t.Fatalf("FenAfter(h2e2) = (%q,%v), want %q", got, ok, want)
	}
	// 迭代两步：黑跳左马（b9=(1,0) 黑马——黑底 rank 9 对应 y=0——到 c7=(2,2)，
	// 腿 (1,1) 空）验证链式演算
	next, ok := FenAfter(got, "b9c7")
	if !ok {
		t.Fatal("黑方 b9c7 应合法")
	}
	// 行 0 黑马离位 "r1bakabnr"；行 2 原 "1c5c1"（x1 黑炮），马落 x2 = "1cn4c1"
	want2 := "r1bakabnr/9/1cn4c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR w - - 0 1"
	if next != want2 {
		t.Fatalf("第二步 FEN = %q, want %q", next, want2)
	}
	// 非法着法拒绝："e9e6" 起子是黑将 (4,0) 而开局红方行棋——非行棋方的子
	// 直接拒（即便换黑行棋，将一步也不能走到 (4,3)，双重不合法）
	if _, ok := FenAfter(startFen, "e9e6"); ok {
		t.Error("帅出宫应拒绝")
	}
}

// PseudoMoves 测试专用：枚举 redGo 一方的全部伪合法走子（不含照面步），
// 供棋规覆盖测试全盘扫描用。生产代码只走单子枚举（pieceTargets，经
// DiffSingleMove/FenAfter），全盘枚举不进生产构建。
// 顺序无契约——测试只断言集合，不断言顺序。
func PseudoMoves(b book.Board, redGo bool) []Move {
	var out []Move
	for y := 0; y < 10; y++ {
		for x := 0; x < 9; x++ {
			p := b[y][x]
			if p == book.Empty || IsRed(p) != redGo {
				continue
			}
			from := Vec{x, y}
			for _, to := range pieceTargets(b, from, p) {
				out = append(out, Move{From: from, To: to})
			}
		}
	}
	return out
}

// TestPlausiblePair 回合结构防线判据（2026-11-10 优势崩塌归因）：用 11-10
// 凌晨局3 的真实故障 FEN 回放——幽灵帧与三子蒸发帧必须被拒，合法两步合并/
// 单步/悔棋还原/仅轮次翻转必须通过。
func TestPlausiblePair(t *testing.T) {
	// 幽灵帧（局3:L268→L272）：兑子两回合合成一帧、已离场红炮残留——同侧
	// 2 格差=黑车 f6→f7"吃自家炮"伪非法形状
	ghostPrev := "3rkab2/4a2R1/4bc3/p1R1C4/1c3rpnp/9/P4C2P/2N1B4/4A4/2BAK4 w - - 0 1"
	ghostCur := "3rkab2/4a2R1/4br3/p1R1C4/1c4pnp/9/P4C2P/2N1B4/4A4/2BAK4 w - - 0 1"
	if reason := PlausiblePair(ghostPrev, ghostCur); reason == "" {
		t.Fatal("幽灵帧必须被拒（同侧 2 格差不可能）")
	}
	// 三子蒸发帧（PC:41586）：红车+红马+黑象无落点消失——同侧 3 格差、子数 -3
	cliffPrev := "4ka3/4a4/2R1b4/p4R3/3N2b2/4r1B1p/9/4C4/9/2BAKn3 w - - 0 1"
	cliffCur := "4ka3/4a4/2R1b4/p8/9/4r1B1p/9/4C4/9/2BAKn3 w - - 0 1"
	if reason := PlausiblePair(cliffPrev, cliffCur); reason == "" {
		t.Fatal("三子蒸发帧必须被拒（子数差超上限）")
	}
	// 合法两步合并（PC:41454 真实帧）：车吃马 + 炮移位，必须通过
	mergePrev := "1r2kab2/4a4/4br1cn/p1nRp1p2/8p/2c1P1P2/PC6P/4C4/N8/2BAKABR1 w - - 0 1"
	mergeCur := "1r2kab2/4a4/4br1cn/p1R1p1p2/2c5p/4P1P2/PC6P/4C4/N8/2BAKABR1 w - - 0 1"
	if reason := PlausiblePair(mergePrev, mergeCur); reason != "" {
		t.Fatalf("合法两步合并不应被拒: %s", reason)
	}
	// 翻转一步：非吃子/吃子/悔棋还原全部通过
	start := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	afterCannon, _ := FenAfter(start, "h2e2")
	if reason := PlausiblePair(start, afterCannon); reason != "" {
		t.Fatalf("翻转一步非吃子应通过: %s", reason)
	}
	afterHorse, _ := FenAfter(afterCannon, "b9c7")
	if reason := PlausiblePair(afterCannon, afterHorse); reason != "" {
		t.Fatalf("翻转一步应通过: %s", reason)
	}
	// 悔棋还原（afterHorse→afterCannon 反向）：出现+换色形态
	if reason := PlausiblePair(afterHorse, afterCannon); reason != "" {
		t.Fatalf("悔棋还原应通过: %s", reason)
	}
	// 兑子合并帧（11-10 局3 真实物理结果：红炮 f3×f7、黑车 f5×f7——cells=3、
	// 子数差 -2）：最高频合法形态，vc47 首版上界错一档曾整批误拒，必须通过
	exchangePrev := "3rkab2/4a2R1/4bc3/p1R1C4/1c3rpnp/9/P4C2P/2N1B4/4A4/2BAK4 w - - 0 1"
	exchangeCur := "3rkab2/4a2R1/4br3/p1R1C4/1c4pnp/9/P7P/2N1B4/4A4/2BAK4 w - - 0 1"
	if reason := PlausiblePair(exchangePrev, exchangeCur); reason != "" {
		t.Fatalf("兑子合并帧不应被拒: %s", reason)
	}
	// 同局面仅轮次翻转（0 格）：手机"更新"/重锚重发
	startB := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	if reason := PlausiblePair(start, startB); reason != "" {
		t.Fatalf("仅轮次翻转应通过: %s", reason)
	}
	// 同侧 1 格（识别自纠：同格换型误读读回/幽灵清理）——2026-11-10 复审对齐
	// 手机端无侧判据：PC 拒掉会形成"重探重读同盘面→SAME_BOARD 不再上行"死端
	sameSideOneCell := "rnbakabnr/9/1c5c1/2p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	if reason := PlausiblePair(start, sameSideOneCell); reason != "" {
		t.Fatalf("同侧 1 格自纠帧应放行: %s", reason)
	}
	// 同型兑子合并帧（cells=2 异色双消失：黑车吃红车、另一黑车回吃红车，接触
	// 格 old==new 无 diff）——兑车最高频合法形态，复审 P1-1 补钉
	sameTypePrev := "r3ka3/9/4p4/r8/9/9/R8/9/9/4K4 w - - 0 1"
	sameTypeCur := "4ka3/9/4p4/r8/9/9/9/9/9/4K4 w - - 0 1"
	if reason := PlausiblePair(sameTypePrev, sameTypeCur); reason != "" {
		t.Fatalf("同型兑子合并帧不应被拒: %s", reason)
	}
	// 同色双消失（纯蒸发签名）维持拒绝
	sameColorVanish := "4ka3/9/4p4/9/9/9/9/9/9/4K4 w - - 0 1"
	if reason := PlausiblePair(sameTypePrev, sameColorVanish); reason == "" {
		t.Fatal("同色双消失必须被拒")
	}
	// 还原形态复原子同色（非悔棋形状）拒绝
	afterCannonB := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"
	restoreSameColor := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2P2C1/9/RNBAKABNR w - - 0 1"
	if reason := PlausiblePair(afterCannonB, restoreSameColor); reason == "" {
		t.Fatal("还原同色子必须被拒")
	}
	// 翻转 ≥3 格不可能（一步恰 2 格）：h2e2 两格 + 黑车凭空消失
	flipThree := "1nbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"
	if reason := PlausiblePair(start, flipThree); reason == "" {
		t.Fatal("翻转 3 格差必须被拒")
	}
	// 畸形 FEN 放行（专用合法性校验负责拒绝）
	if reason := PlausiblePair("bad", start); reason != "" {
		t.Fatalf("畸形 FEN 应放行给专用校验: %s", reason)
	}
}

// TestPlausiblePair_StartJump 跨局重开放行（2026-11-12 #5 实测误伤）：识别端
// NEW_GAME 判定失效时，上一局残局 → 新局初始盘面的大跳变无 new_game 标记，
// 结构门无放行形态必拒——重新开局是物理合法跳变，盘面段与标准初始布局全等
// 即放行；残缺开局（识别错位拼出的残盘）不放，交重探。
func TestPlausiblePair_StartJump(t *testing.T) {
	endgame := "3ak1b2/4a4/1R1cb1n2/2pnpCN1p/p8/2P1P1P2/P8/N3B4/3rA4/3AK1B2 w - - 0 1"
	if reason := PlausiblePair(endgame, startFen); reason != "" {
		t.Fatalf("残局→开局跳变应放行: %s", reason)
	}
	// 残缺开局（缺双炮与红炮）：不得按开局跳变放行
	brokenStart := "rnbakabnr/9/9/p1p1p1p1p/9/9/P1P1P1P1P/9/9/RNBAKAB1R b - - 0 1"
	if reason := PlausiblePair(endgame, brokenStart); reason == "" {
		t.Fatal("残缺盘面不得按开局跳变放行")
	}
}
