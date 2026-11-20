<!--
PR 前必读：CONTRIBUTING.md 与 DESIGN_BOUNDARIES.md §6（永远不做）。
负面清单类改动（自动执行/注入/反检测/机器可读输出接口等）会被直接关闭并留档。
-->

## 改动内容

<!-- 一段话说清：改了什么、为什么、影响面（协议/双端/构建） -->

## 边界自查

- [ ] 本改动不涉及 DESIGN_BOUNDARIES.md §6 负面清单（自动执行 / 触摸注入 /
      无障碍模拟 / 反检测 / 机器可读输出接口 / 静默录屏 / 多余敏感权限）
- [ ] 协议改动已先更新 docs/protocol-v1.md，旧端可忽略未知字段
- [ ] 本地门禁全绿：`go test ./...` + `golangci-lint run`（Go 侧）
      / `:app:testArmv8DotprodDebugUnitTest`（Android 侧）
- [ ] 不含版权物（引擎 / NNUE / 开局库 / 识别模型）与本机隐私信息

## 关联

<!-- 关联 issue / 已知问题，无则删去本节 -->
