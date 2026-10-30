package com.cabinet.utils;

import android.graphics.Matrix;
import android.graphics.Path;

/*
 * ArrowShape.java
 * 设置箭头的参数，然后生成箭头的Path
 * 具体的参数可以参考这个文档：
 * https://github.com/zfdang/chinese-chess-android/blob/master/docs/%E7%BB%98%E5%88%B6%E7%AE%AD%E5%A4%B4.md
 * Created by zfdang on 2018-03-25
 */
public class ArrowShape {
    /** 箭尖半角：单位特殊，实际角度(度) = 值/2（内部按 angle/360·π 转弧度），勿按普通角度理解 */
    float angle;
    /** 箭翼内角：单位同 angle 的双倍角度制（悬浮窗传 60/120 即实际 30°/60°） */
    float innerAngle;
    /** 箭尖到翼折点的斜边长（决定箭头在行程中的占比） */
    float sideLength;
    /** 箭头两翼间距（最宽处） */
    float headWidth;
    /** 箭尾宽度（箭杆厚度） */
    float tailWidth;

    /** 变换矩阵复用（getTransformedPath 每次调用先 reset，见方法尾部注释） */
    private final Matrix mtx = new Matrix();

    public ArrowShape(float angle, float innerAngle, float length, float sideLength, float headWidth, float tailWidth) {
        this.angle = angle;
        this.innerAngle = innerAngle;
        this.sideLength = sideLength;
        this.headWidth = headWidth;
        this.tailWidth = tailWidth;
    }

    /**
     * 生成从 (x0,y0) 指向 (x1,y1) 的箭头路径。
     * 先在局部坐标系构建形状（箭尾在原点、箭尖在 (起止点距离, 0)，指向 +x，
     * 长度即起止点实际距离），再按起止方向 atan2 旋转并平移到 (x0,y0)。
     */
    public void getTransformedPath(Path path, float x0, float y0, float x1, float y1) {

        double angle1 = this.angle / 360 * Math.PI;
        double angle2 = this.innerAngle / 360 * Math.PI;
        float sinv1 = (float) Math.sin(angle1);
        float cosv1 = (float) Math.cos(angle1);
        float tanv2 = (float) Math.tan(angle2);

        // 直接设置箭头的长度为目标长度，避免缩放时箭头变形
        float x2 = (float) Math.hypot(x1 - x0, y1 - y0);
        float y2 = 0;

        // (x3, y3)
        float x3 = (float) (x2 - sideLength * cosv1);
        float y3 = (float) (y2 - sideLength * sinv1);

        // (x5, y5)
        float x5 = (float) (x3 + sideLength * sinv1 / tanv2 - headWidth / 2 / tanv2);
        float y5 = -headWidth / 2;

        // (x6, y6)
        float x6 = 0;
        float y6 = -tailWidth / 2;

        path.reset();
        path.moveTo(x2, y2);
        path.lineTo(x3, y3);
        path.lineTo(x5, y5);
        path.lineTo(x6, y6);
        path.lineTo(x6, -y6);
        path.lineTo(x5, -y5);
        path.lineTo(x3, -y3);
        path.close();

        // Matrix 复用：本类只被悬浮窗重绘热路径调用（UI 线程单线程无并发），
        // 每次变换 new Matrix() 是纯浪费；reset 防止上次的旋转/平移叠加
        mtx.reset();
        mtx.postRotate((float) (Math.atan2(y1 - y0, x1 - x0) * 180 / Math.PI));
        mtx.postTranslate(x0, y0);
        path.transform(mtx);
    }
}
