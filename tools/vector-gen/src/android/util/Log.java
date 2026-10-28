package android.util;

/**
 * vector-gen 桌面环境的 android.util.Log 桩：拷贝源（Board.java 等）引用了 Log，
 * 纯 Java 环境下以空实现替代，不改拷贝的源文件。
 */
public class Log {
    public static int e(String tag, String msg) {
        System.err.println("[" + tag + "] " + msg);
        return 0;
    }

    public static int w(String tag, String msg) {
        System.err.println("[" + tag + "] " + msg);
        return 0;
    }

    public static int i(String tag, String msg) {
        System.out.println("[" + tag + "] " + msg);
        return 0;
    }

    public static int d(String tag, String msg) {
        return 0;
    }
}
