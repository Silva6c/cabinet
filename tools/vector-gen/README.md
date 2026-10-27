# vector-gen：桔库查询向量生成器（Java 端权威实现）

> 桔库数据出处：象棋鱼 zfdang/chinese-chess-fish-android（MIT，
> https://github.com/zfdang/chinese-chess-fish-android）；生成的 testdata/
> ju-book-*.json 为其衍生向量，分发时保留上游版权声明即可。

对固定局面集计算 Zobrist 哈希并查询桔库，输出 JSON 向量到 `testdata/ju-book-vectors.json`，
供 Go 移植版（`internal/book`）做双端一致性比对（`TestHashVectors` / `TestQueryVectors`）。

## 目录

```
vector-gen/
├─ src/com/cabinet/chess/gamelogic/   # 作者先前象棋项目只读拷贝（Zobrist/Board/Move/Piece/Position）
├─ src/com/cabinet/chess/openbook/    # 作者先前象棋项目只读拷贝（BookData/OpenBook/OpenBookBase；
│                                  #   BHOpenBook/BHDatabase 依赖 Android，仅作对照不参与编译）
├─ src/android/util/Log.java       # android.util.Log 空实现桩（不改拷贝的源文件）
├─ src/Main.java                   # 向量生成器（SQL 逐字照抄 BHOpenBook.java:27-32）
└─ lib/sqlite-jdbc.jar             # sqlite-jdbc 驱动（不进 git，见下）
```

## 编译运行

依赖：JDK 8+、sqlite-jdbc 3.46.1.3（Apache-2.0，org.xerial；下载后放 `lib/sqlite-jdbc.jar`，版本无强约束；jar 不入库不分发）：

```bash
# 下载驱动（代理已开；或从 Maven Central 手动取）
curl -L -x http://127.0.0.1:7897 -o lib/sqlite-jdbc.jar \
  https://repo1.maven.org/maven2/org/xerial/sqlite-jdbc/3.46.1.3/sqlite-jdbc-3.46.1.3.jar

# 编译（列出源文件；BHOpenBook/BHDatabase 不编译）
mkdir -p out
javac -encoding UTF-8 -d out \
  src/android/util/Log.java \
  src/com/cabinet/chess/gamelogic/*.java \
  src/com/cabinet/chess/openbook/BookData.java \
  src/com/cabinet/chess/openbook/OpenBook.java \
  src/com/cabinet/chess/openbook/OpenBookBase.java \
  src/Main.java

# 生成向量（桔库先行解压为 .obk SQLite 文件，放 library/books/）
java -cp "out;lib/sqlite-jdbc.jar" -Dfile.encoding=UTF-8 Main \
  "<仓库根>/library/books/<自备桔库>.obk" \
  "<仓库根>/testdata/ju-book-vectors.json"
```

## 覆盖用例

红先起始、同盘黑先（zobristPlayer 项）、真实开局推演序列 9 步（红黑先交替）、
非对称局面左右镜像（红/黑先）、空查询（光帅对光将，必未命中）。

向量含未命中用例是刻意设计：Go 侧必须复现"相同哈希 + 相同查空"才算一致。
