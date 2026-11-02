// sections.go 面板分区的构建与刷新（便当盒布局，棋韵 §5.3）：
// 折叠态常显：01 引擎 tile / 02 开局库 tile / 03 连接 tile / 04 走势 tile /
// 05 高级功能按钮排；高级面板（按钮排展开）：引擎调参/建议流/开局库详情/
// 日志/检查更新。
package console

import (
	"errors"
	"fmt"
	"image/color"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"cabinet/internal/update"
	"cabinet/internal/xq"
)

// ---------- 01 引擎 tile ----------

// sectionEngine 引擎 tile（bento 左）：分区头 + 两行三格大数字（状态/深度/速率、
// 节点/哈希/配置）+ 一行 kv（模式/今日建议）。大数字 sans Bold（含中文与正文
// 同族，见 statBig 雷区注释）。
//
// 原"皮卡鱼 · UCI"caption 位曾于 2026-10-29 短暂挂过三枚状态 chip，2026-10-30
// 用户定稿删除（信息与 01"状态"/02"收录局面"/03"N 台"大数字完全重复），恢复
// caption 占位。
func (c *Console) sectionEngine() fyne.CanvasObject {
	mk := func(key string) fyne.CanvasObject {
		o, v := kvCell(key)
		c.engCells[key] = v
		return o
	}
	mkMeter := func(key string, col color.Color) fyne.CanvasObject {
		o, ls, v, vs := statBig(key, col)
		c.engLabelCells[key] = ls
		c.engCells[key] = v
		c.engSufCells[key] = vs
		return o
	}
	row1 := container.NewGridWithColumns(3,
		mkMeter("状态", qiJade), // 就绪 = 玉（与手机端状态语言同源）
		mkMeter("深度 / 目标", qiForeground),
		mkMeter("搜索速率", qiForeground))
	row2 := container.NewHBox(mk("节点"), mk("哈希"), mk("配置"))
	// row3（2026-10-30 补空）：模式（深度制/时间制·档位）与今日沉淀条数——
	// 前者与"深度/目标"仪表互补（时间制下仪表不显示深度目标，这里给档位），
	// 后者不用展开建议流面板也一眼可见（与面板 sugCount 同源 countToday）
	row3 := container.NewHBox(mk("模式"), mk("今日建议"))
	return bentoTile(container.NewVBox(
		sectionHeader("01", "引擎", qiCaption("皮卡鱼 · UCI")),
		row1,
		row2,
		row3,
	))
}

// ---------- 02 开局库 tile ----------

// tileBooks 开局库 tile（bento 右）：收录局面大数字 + 今日命中率进度条 + 明细。
// 数据来自 Options.Books（库文件）与 Options.Recent（今日沉淀的来源比例）。
func (c *Console) tileBooks() fyne.CanvasObject {
	head, headSet := qiDynamic("—", qiSize(theme.SizeNameCaptionText), qiMuted, false, false)
	total, totalSet := qiDynamic("0", qiSize(meterSizeName), qiForeground, false, true)
	rate, rateSet := qiDynamic("—", qiSize(theme.SizeNameText), qiForeground, false, true)
	sub, subSet := qiDynamic("最近更新 —", qiSize(theme.SizeNameCaptionText), qiMuted, false, false)
	bar, barSet := newHBar()
	c.bookHeadSet, c.bookTotalSet, c.bookRateSet, c.bookSubSet, c.bookBarSet = headSet, totalSet, rateSet, subSet, barSet
	rows := []fyne.CanvasObject{
		sectionHeader("02", "开局库", head),
		qiCaption("收录局面"),
		total,
		bar,
		container.NewHBox(qiCaption("今日命中率"), rate, layout.NewSpacer(), sub),
	}
	// 云库开关行（可选外部开局库；nil 回调 = 行缺席）。说明小字随"默认走云库"
	// 切换措辞（cloudLookupFirst 语义矩阵的 UI 面，静态文案会失真）
	if c.opts.CloudBookGet != nil {
		c.cloudChk = widget.NewCheck("云库查询", func(on bool) {
			if c.cloudBusy.Load() || c.opts.CloudBookSet == nil {
				return
			}
			if msg := c.opts.CloudBookSet(on); msg != "" {
				dialog.NewError(errors.New(msg), c.win).Show()
			}
		})
		endpoint := c.opts.CloudBookEndpoint
		if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
			endpoint = u.Host
		}
		cloudHint, cloudHintSet := qiDynamic("本地库/沉淀未命中才查", qiSize(theme.SizeNameCaptionText), qiMuted, false, false)
		c.cloudHintSet = cloudHintSet
		rows = append(rows, container.NewHBox(
			c.cloudChk,
			qiCaption(endpoint),
			layout.NewSpacer(),
			cloudHint,
		))
	}
	// 残局库开关不放 02 tile（2026-11-02 用户定稿：与云库开关并排显重复）——
	// 高级面板→开局库的单份开关统一管理
	return bentoTile(container.NewVBox(rows...))
}

// updateBookTile 回填开局库 tile：收录总局面 + 今日命中率 + 最近更新
func (c *Console) updateBookTile(books []BookRow) {
	if c.bookTotalSet == nil {
		return
	}
	total := 0
	var newest time.Time
	for _, b := range books {
		total += b.Entries
		if b.ModTime.After(newest) {
			newest = b.ModTime
		}
	}
	if len(books) == 0 {
		c.bookHeadSet("未发现 .obk")
	} else {
		c.bookHeadSet(books[0].Name + " · 热加载")
	}
	c.bookTotalSet(fmtInt(int64(total)))
	c.bookSubSet("最近更新 " + fmtDay(newest))
	if rate, ok := c.todayBookRate(); ok {
		c.bookRateSet(fmt.Sprintf("%.0f%%", rate*100))
		c.bookBarSet(rate)
	} else {
		c.bookRateSet("—")
		c.bookBarSet(0)
	}
}

// refreshCloudBook 回写云库开关勾选态（cloudBusy 抑制 SetChecked 的 OnChanged
// 回环——fyne SetChecked 会主动回调，与 tunerBusy 同款套路）。
func (c *Console) refreshCloudBook() {
	if c.cloudChk == nil || c.opts.CloudBookGet == nil {
		return
	}
	on := c.opts.CloudBookGet()
	if c.cloudChk.Checked == on {
		return
	}
	c.cloudBusy.Store(true)
	defer c.cloudBusy.Store(false)
	c.cloudChk.SetChecked(on)
}

// refreshCloudFirst 回写"默认走云库"勾选态与 02 tile 云库说明措辞
// （cloudFirstBusy 抑制 SetChecked 回环，与 refreshCloudBook 同款套路）。
// 措辞随开关切换：cloudLookupFirst 语义矩阵的 UI 面——cloud_first 开启后
// 云库命中即终，"未命中才查"的旧说法会失真。
func (c *Console) refreshCloudFirst() {
	if c.cloudHintSet != nil {
		if c.opts.CloudFirstGet != nil && c.opts.CloudFirstGet() {
			c.cloudHintSet("云库优先：命中即用，未命中才走本地")
		} else {
			c.cloudHintSet("桔库/沉淀未命中才查")
		}
	}
	if c.cloudFirstChk == nil || c.opts.CloudFirstGet == nil {
		return
	}
	on := c.opts.CloudFirstGet()
	if c.cloudFirstChk.Checked == on {
		return
	}
	c.cloudFirstBusy.Store(true)
	defer c.cloudFirstBusy.Store(false)
	c.cloudFirstChk.SetChecked(on)
}

// refreshEndgame 回写"残局库查询"勾选态（endgameBusy 抑制 SetChecked 回环，
// 与 refreshCloudFirst 同款套路）。单一实例（02 tile 副本已于 2026-11-02 删除）。
func (c *Console) refreshEndgame() {
	if c.endgameChk == nil || c.opts.EndgameGet == nil {
		return
	}
	on := c.opts.EndgameGet()
	if c.endgameChk.Checked == on {
		return
	}
	c.endgameBusy.Store(true)
	defer c.endgameBusy.Store(false)
	c.endgameChk.SetChecked(on)
}

// refreshSkipBooks 回写"不走任何库"勾选态（skipBooksBusy 抑制 SetChecked 回环，
// 与 refreshCloudFirst 同款套路）。无联动文案：02 tile 云库说明不随本开关切换
// （它描述的是云库自身维度；本开关的提示在勾选行 caption 与代码注释里）。
func (c *Console) refreshSkipBooks() {
	if c.skipBooksChk == nil || c.opts.SkipBooksGet == nil {
		return
	}
	on := c.opts.SkipBooksGet()
	if c.skipBooksChk.Checked == on {
		return
	}
	c.skipBooksBusy.Store(true)
	defer c.skipBooksBusy.Store(false)
	c.skipBooksChk.SetChecked(on)
}

// todayBookRate 今日命中率：今天沉淀的建议里开局库来源占比（今天无样本返回 false）。
// 取样最近 200 条（跨天时自然只剩今天的条目参与统计）。200 的存在理由：复用
// 已注入的 Recent 回调，避免为命中率再开一路"按天按来源计数"的专用查询；今日
// 条目超过 200 时分母退化为"最近 200 条内的今日占比"（仅仪表参考，可接受）。
func (c *Console) todayBookRate() (float32, bool) {
	if c.opts.Recent == nil {
		return 0, false
	}
	ty, tm, td := time.Now().Date()
	var book, all int
	for _, r := range c.opts.Recent(200) {
		y, m, d := r.TS.Date()
		if y != ty || m != tm || d != td {
			continue
		}
		all++
		if strings.HasPrefix(r.Source, "book:") {
			book++
		}
	}
	if all == 0 {
		return 0, false
	}
	return float32(book) / float32(all), true
}

// ---------- 引擎调参面板 ----------
// （圈号分节 ② 连接 / ③ 建议流 / ④ 库区 / ⑤ 日志 / ⑥ 检查更新被
// docs/protocol-v1.md、docs/technical-overview.md 引用，勿重排；
// 本面板不占编号，避免与 ② 连接撞号）

// 调参下拉选项（协议 §4.10 范围内的常用档；"自动"= 0）。
var (
	tunerThreadOpts = []string{"自动", "2", "4", "6", "8", "10", "12", "14", "16", "18", "20", "24", "32", "48", "64"}
	tunerMPOpts     = []string{"1", "2", "3", "4", "5"}
	// Hash 档封顶 10GB（2026-10-01 用户钉死：32GB 机器给引擎 10GB 置换表已是上限）
	tunerHashOpts  = []string{"自动", "256MB", "512MB", "1GB", "2GB", "4GB", "8GB", "10GB"}
	tunerDepthOpts = []string{"8", "12", "16", "20", "24", "28", "32", "36", "40"}
	// 时间上限档（2026-10-28 思考模式定稿）："不限" = 按深度（movetime=0），
	// 其余为每步 go movetime 的毫秒档。
	tunerMoveTimeOpts = []string{"不限（按深度）", "1秒", "3秒", "5秒", "10秒", "20秒"}
)

// sectionTuner 引擎调参面板：线程数 / MultiPV / Hash / 深度四下拉 + 清空置换表。
// 改动经 main 注入的 TunerSet（与手机端 set_engine 同一条协议路径）即时生效；
// 引擎桥不可调（TunerGet 为 nil）时返回 nil，按钮排中"引擎调参"一并隐藏。
// 当前值不在档内时下拉直接显示该值。
func (c *Console) sectionTuner() fyne.CanvasObject {
	if c.opts.TunerGet == nil || c.opts.TunerSet == nil {
		return nil
	}
	mk := func(kind string, opts []string) *widget.Select {
		s := widget.NewSelect(opts, func(string) { c.onTunerChanged(kind) })
		s.PlaceHolder = "—"
		c.tunerSelects[kind] = s
		c.tunerBaseOpts[kind] = opts
		return s
	}
	th := mk("threads", tunerThreadOpts)
	mp := mk("multipv", tunerMPOpts)
	hs := mk("hash", tunerHashOpts)
	dp := mk("depth", tunerDepthOpts)
	mt := mk("movetime", tunerMoveTimeOpts)
	clearBtn := newHiliteButton("清空置换表", c.onClearHash)

	hint := widget.NewLabel("改动即时以最近局面重新搜索；与手机端调节双向同步")
	c.tunerHint = hint // refreshTuner 逐轮追加当前思考模式（含手机端远程调节的跟随回显）
	hint.TextStyle = fyne.TextStyle{Italic: true}
	hint.Importance = widget.LowImportance

	row := container.NewGridWithColumns(5,
		container.NewVBox(widget.NewLabel("线程数"), th),
		container.NewVBox(widget.NewLabel("变招路数"), mp),
		container.NewVBox(widget.NewLabel("置换表"), hs),
		container.NewVBox(widget.NewLabel("搜索深度"), dp),
		container.NewVBox(widget.NewLabel("时间上限"), mt),
	)
	// 智能思考开关（2026-11-02 用户定稿，仅 PC 端）：两级自适应——被迫着法
	// 预算压地板秒出 + 主线收敛提前收束（机制见 engine/smart.go）。非协议参数：
	// 手机端无感知、不加按钮；热切经 TunerSet("smart") 走 TuneEngine 统一通道
	//（应用 + reSearchLatest + 落盘），与五下拉同一事实源。放头部行与清空置换表
	// 并排：五面板统一盒高 220 竖向无余量（新起一行会撑破，advpanel_test 锚死），
	// 说明全文收进行尾 ⓘ
	c.smartChk = widget.NewCheck("智能思考", func(on bool) {
		if c.smartBusy.Load() {
			return
		}
		v := 0
		if on {
			v = 1
		}
		if msg := c.opts.TunerSet("smart", v); msg != "" {
			dialog.ShowError(errors.New(msg), c.win)
		}
		c.refreshTuner() // 立即回显服务端实际接受值（失败自动弹回）
	})
	return container.NewVBox(
		sectionTitle("引擎调参", container.NewHBox(
			c.smartChk,
			newInfoChip("关于\"智能思考\"",
				"开启后按局面与搜索收敛度自适应分配算力：唯一/极少着法的局面本轮直接低预算秒出；"+
					"主线着法连续多层稳定且分数平稳即提前收束（速度优先，深度略浅；杀棋局面更快）。\n\n"+
					"思考上限仍受当前深度制/时间制约束，可随时关闭。仅电脑端生效——手机端无感知、无按钮，协议零改动。",
				c.win),
			clearBtn,
		)),
		row,
		hint,
		widget.NewSeparator(),
	)
}

// onTunerChanged 下拉选中（用户操作）：解析档位值并下发；失败恢复显示当前实际值。
// 刷新回写 Selected 也会触发本回调——用 tunerBusy 抑制回环。
func (c *Console) onTunerChanged(kind string) {
	if c.tunerBusy.Load() {
		return
	}
	sel := c.tunerSelects[kind]
	if sel == nil || sel.Selected == "" {
		return
	}
	v, err := parseTunerOption(kind, sel.Selected)
	if err != nil {
		return
	}
	if msg := c.opts.TunerSet(kind, v); msg != "" {
		dialog.ShowError(errors.New(msg), c.win)
	}
	c.refreshTuner() // 立即回显服务端实际接受值
}

// onClearHash 清空置换表按钮。
func (c *Console) onClearHash() {
	if c.opts.TunerClearHash == nil {
		return
	}
	if msg := c.opts.TunerClearHash(); msg != "" {
		dialog.ShowError(errors.New(msg), c.win)
		return
	}
	dialog.ShowInformation("已受理", "置换表将在下一轮搜索开始前清空", c.win)
}

// parseTunerOption 档位文本 → 数值："自动" → 0（threads/hash 的自动档语义），
// "不限（按深度）" → 0（movetime 的时间制关闭语义），"Ns" → N*1000 毫秒，
// 其余解析整数；"1GB" 等 Hash 简写换算 MB。非法返回错误。
func parseTunerOption(kind, s string) (int, error) {
	if s == "自动" {
		if kind != "threads" && kind != "hash" {
			return 0, fmt.Errorf("%s 不支持自动档", kind)
		}
		return 0, nil
	}
	if kind == "movetime" {
		if s == "不限（按深度）" {
			return 0, nil
		}
		t := strings.TrimSuffix(s, "秒")
		v, err := strconv.Atoi(t)
		if err != nil {
			return 0, fmt.Errorf("时间上限档非法 %q", s)
		}
		return v * 1000, nil
	}
	t := strings.TrimSpace(strings.TrimSuffix(s, "MB"))
	mult := 1
	if strings.HasSuffix(t, "GB") {
		t = strings.TrimSuffix(t, "GB")
		mult = 1024
	}
	v, err := strconv.Atoi(t)
	if err != nil {
		return 0, err
	}
	return v * mult, nil
}

// tunerOptionText 数值 → 档位文本（调参区回显；与 parseTunerOption 互逆）。
func tunerOptionText(kind string, v int) string {
	if v <= 0 && (kind == "threads" || kind == "hash") {
		return "自动"
	}
	if kind == "movetime" {
		if v <= 0 {
			return "不限（按深度）"
		}
		return fmt.Sprintf("%d秒", v/1000)
	}
	if kind == "hash" {
		if v >= 1024 && v%1024 == 0 {
			return fmt.Sprintf("%dGB", v/1024)
		}
		return fmt.Sprintf("%dMB", v)
	}
	return fmt.Sprintf("%d", v)
}

// refreshTuner 回写调参区当前值（tunerBusy 抑制 SetSelected 触发的 OnChanged 回环）。
//
// 回环时序：fyne 的 Select.SetSelected 会同步重入 OnChanged（v2.8.1 select.go
// updateSelected 直接回调），busy=true 窗口覆盖下面 5 次 SetSelected 的同步重入。
// atomic 够用的前提是"用户点击回调与刷新都串行在 fyne 主 goroutine"（刷新经
// fyne.Do 排队）——两路天然互斥，无并发窗口；若 refresh 脱离 fyne.Do 直接在
// 后台调用，此标志即失效，必须换互斥锁。
func (c *Console) refreshTuner() {
	if c.opts.TunerGet == nil {
		return
	}
	ts := c.opts.TunerGet()
	c.tunerBusy.Store(true)
	defer c.tunerBusy.Store(false)
	set := func(kind, text string, enabled bool) {
		s, ok := c.tunerSelects[kind]
		if !ok {
			return
		}
		// 档外值兜底：fyne 的 SetSelected 只在 text 命中 Options 时生效
		//（v2.8.2 select.go 逐字核实），否则静默空转、下拉残留上一个参数的
		// 旧值——而服务端合法范围远宽于快捷档（深度 1-40 全收/时间任意秒/
		// Hash 任意 MB，手机端可下任意值）。回显 = 基础快捷档 + 当前值并入，
		// 下拉如实显示当前值而不是撒谎。Selected 等值短路：Select 的
		// updateSelected 无判重，1.5s 轮询常态是值没变，等值时零重绘。
		if s.Selected != text {
			base := c.tunerBaseOpts[kind]
			if !slices.Contains(base, text) {
				base = append(slices.Clone(base), text)
			}
			s.Options = base
			s.SetSelected(text)
		}
		s.Enable()
		if !enabled {
			s.Disable()
		}
	}
	set("threads", tunerOptionText("threads", ts.Threads), ts.Ready)
	set("multipv", tunerOptionText("multipv", ts.MultiPV), ts.Ready)
	set("hash", tunerOptionText("hash", ts.HashMB), ts.Ready)
	set("depth", tunerOptionText("depth", ts.Depth), ts.Ready)
	set("movetime", tunerOptionText("movetime", ts.MoveTimeMs), ts.Ready)
	// 当前思考模式动态提示（时间制优先于深度——手机端芯片远程调节时此行即
	// PC 侧的"以谁为准"回显，1.5s 轮询自动跟随）
	if c.tunerHint != nil {
		mode := fmt.Sprintf("当前：深度制 %d 层", ts.Depth)
		if ts.MoveTimeMs > 0 {
			mode = fmt.Sprintf("当前：时间制 %d 秒", ts.MoveTimeMs/1000)
		}
		c.tunerHint.Text = "改动即时以最近局面重新搜索；与手机端调节双向同步 · " + mode
		c.tunerHint.Refresh()
	}
	// 智能思考勾选态回写（smartBusy 抑制 SetChecked 的 OnChanged 回环，
	// cloudBusy 同款套路）
	if c.smartChk != nil && c.smartChk.Checked != ts.Smart {
		c.smartBusy.Store(true)
		c.smartChk.SetChecked(ts.Smart)
		c.smartBusy.Store(false)
	}
}

// ---------- 高级面板按钮排 ----------

// sectionAdvancedBar 高级功能按钮排：基础区只保留引擎/连接两块，调参/建议流/
// 开局库/日志收进本按钮排（toggleAdvanced 展开一次一个）。面板内容在此提前构建；
// 构建结果为 nil 的面板（如引擎桥不可调）按钮一并隐藏。
func (c *Console) sectionAdvancedBar() fyne.CanvasObject {
	c.advPanels["tuner"] = wrapBento(c.sectionTuner())
	c.advPanels["suggestions"] = wrapBento(c.sectionSuggestions())
	c.advPanels["books"] = wrapBento(c.sectionBooks())
	c.advPanels["log"] = wrapBento(c.sectionLog())
	c.advPanels["update"] = wrapBento(c.sectionUpdate())
	// 用切片收集可用按钮：面板不可用时（如引擎桥不可调）不放入 nil——
	// HBox 里放 typed-nil 会在 MinSize 时 panic
	var btns []fyne.CanvasObject
	addBtn := func(name, label string, icon fyne.Resource) {
		if c.advPanels[name] == nil {
			return
		}
		b := widget.NewButtonWithIcon(label, icon, func() { c.toggleAdvanced(name) })
		b.Importance = widget.MediumImportance
		c.advBtns[name] = b
		btns = append(btns, b)
	}
	addBtn("tuner", "引擎调参", theme.SettingsIcon())
	addBtn("suggestions", "建议流", theme.HistoryIcon())
	addBtn("books", "开局库", theme.StorageIcon())
	addBtn("log", "日志", theme.DocumentIcon())
	addBtn("update", "检查更新", theme.DownloadIcon())
	hint := qiCaption("一次展开一个面板")
	return container.NewVBox(
		sectionHeader("05", "高级功能", hint),
		container.NewHBox(btns...),
	)
}

// ---------- ② 连接区 ----------

// sectionClients 设备列表（v3 表格式便当盒，2026-10-20 定稿）：表头行（会话/
// 客户端/版本/RTT/路径/最后 position）作为公用设施恒在顶部，数据行横排与表头
// 同构对齐——用户否决卡片形态（"不如初版表格顺眼、新连接挤兑其他区块"），
// 但保留卡片化的交互成果：点行选中/再点同行取消/点空白取消（tapCatcher，
// 旧 widget.Table 做不到）。行列表装入固定高滚动区（100px，与旧 fixedTable
// 同量级）——设备再多也只在连接区内部滚动，tile 高度恒定、窗口总高不变。
// 数据仍由 refresh 每 1.5s 回填 c.clientRows；行数变化时重建，数量不变时逐行
// set（避免周期重建造成的视觉抖动）。
func (c *Console) sectionClients() fyne.CanvasObject {
	connText, connSet := qiDynamic("0 台", qiSize(theme.SizeNameCaptionText), qiMuted, false, false)
	c.connCountSet = connSet
	qrBtn := newGlowButton("连接二维码", c.showQRDialog)
	qrBtn.Importance = widget.HighImportance // 朱砂实心（图标的填充色与 tile 底同色，必须提级才可见）
	// 断开会话：选中设备行后可用，点击弹确认（踢掉 = 发 bye 后优雅关闭，
	// 手机端会自动重连，不是拉黑）
	c.kickBtn = newGlowButton("断开会话", c.confirmKickSession)
	c.kickBtn.Importance = widget.DangerImportance
	c.kickBtn.Disable()
	// 证书指纹：设计稿 §06 分组短式 chip（只读核对；完整指纹生成二维码时原样
	// 使用）。空 = 未启用锁定，手机端维持联调模式（不校验证书）。
	c.fpChip = newFpChip()
	c.fpChip.set(fpShort(c.opts.CertFingerprint))

	// 数据行列表 + 底层空白捕获层：Stack 后参在上——点数据行由行收 tap，
	// 点行间/列表下方空白穿透到底层 catcher（selectClient("") 清选中）。
	// fixedTable 可调高滚动区：**恒定 100px**（2026-10-30 用户定稿：连接数变化
	// 不得推移下方区块——1-2 台留白、3 台起盒内滚动，窗口高度全程稳定；原
	// "行数少随内容"方案每加一台就把走势/05 面板顶下去一次）
	c.clientRowsBox = container.New(&vbox{gap: 4})
	// 冷启动空态：syncClientRows 对"0 行 vs 0 行"走零循环不重建，空态提示
	// （只在 rebuild 里创建）会缺席到首台设备连线再断开——构建期先重建一次
	c.rebuildClientRows()
	catcher := newTapCatcher(func() { c.selectClient("") })
	listArea, _ := c.fixedTable(c.clientRowsBox, 100)
	listArea = container.NewStack(catcher, listArea)

	return bentoTile(container.NewVBox(
		sectionHeader("03", "连接", connText, qiCaption("WSS · 自签证书（指纹锁定）")),
		clientHeaderRow(),
		listArea,
		container.NewHBox(qiCaption("证书指纹"), c.fpChip.object()),
		container.NewHBox(qrBtn, c.kickBtn),
	))
}

// rebuildClientRows 按 clientRows 全量重建数据行列表（设备增减时调用）；
// 空列表显示一行灰字空态而非空白。可视区恒定 100px（sectionClients），设备
// 增减只换列表内容、不改区块高度——下方 tile 零位移。
func (c *Console) rebuildClientRows() {
	c.clientRowsBox.RemoveAll()
	if len(c.clientRows) == 0 {
		c.clientRowsBox.Add(qiCaption("暂无连接——手机端连线成功后在此显示"))
	} else {
		for i := range c.clientRows {
			row := c.clientRows[i]
			r := newClientRow(func(fullID string) { c.selectClient(fullID) })
			// UI 树构造期已建好（newClientRow 内），挂树前 set 安全
			r.set(row, row.FullID == c.selectedID)
			c.clientRowsBox.Add(r)
		}
	}
}

// selectClient 单选维护：点已选行再次点击 = 取消；空串 = 清选中（空白点击）。
// 同步"断开会话"按钮启停并重画全部行高亮。
func (c *Console) selectClient(fullID string) {
	if fullID != "" && c.selectedID == fullID {
		fullID = "" // toggle：再点同行取消
	}
	c.selectedID = fullID
	if fullID == "" {
		c.kickBtn.Disable()
	} else {
		c.kickBtn.Enable()
	}
	c.syncClientRows()
}

// syncClientRows 不重建，仅按 selectedID/最新 clientRows 回填每一行
// （数量与 clientRows 一致时走此路径，避免 1.5s 周期重建抖动）。
func (c *Console) syncClientRows() {
	kids := c.clientRowsBox.Objects
	rows := make([]*clientRow, 0, len(kids))
	for _, k := range kids {
		if rr, ok := k.(*clientRow); ok {
			rows = append(rows, rr)
		}
	}
	if len(rows) != len(c.clientRows) {
		c.rebuildClientRows()
		return
	}
	for i, rr := range rows {
		rr.set(c.clientRows[i], c.clientRows[i].FullID == c.selectedID)
	}
}

// fenBrief "最后 position"列内容：FEN 摘要截前 16 字符加省略号（设计稿 §06
// "r1ba1/9/1c2c4/p1…"）；尚未发过局面时退回最近活动时间。
func fenBrief(fen string, last time.Time) string {
	if fen == "" {
		if last.IsZero() {
			return ""
		}
		return fmtTime(last)
	}
	if len(fen) > 16 {
		return fen[:16] + "…"
	}
	return fen
}

// confirmKickSession 断开选中会话：确认弹窗 → 调服务端踢连接 → 复位选中态。
// 手机端收到 bye 后按断线重连处理（自动退避重连），不是封禁。
func (c *Console) confirmKickSession() {
	id := c.selectedID
	if id == "" {
		return
	}
	if c.opts.KickSession == nil {
		dialog.NewInformation("断开会话", "当前构建未接入会话管理，无法断开。", c.win).Show()
		return
	}
	// 确认文案带上会话缩略 ID 与客户端名（若有），断错设备的代价是对方重连一遍
	r := c.rowByID(id)
	// 昵称功能（2026-10-28）：确认文案优先显示设备昵称（一眼认出断的是哪台），
	// 未上报昵称退客户端名，都无则按鉴权状态给占位
	name := "（未鉴权）"
	if r != nil {
		switch {
		case r.Authed && r.Name != "":
			name = r.Name
		case r.Authed && r.Client != "":
			name = r.Client
		case r.Authed:
			name = "（已鉴权）"
		}
	}
	dialog.NewConfirm("断开会话",
		fmt.Sprintf("确定断开 %s（%s）？\n\n手机端会自动重连（不是拉黑）。", id[:min(8, len(id))]+"…", name),
		func(ok bool) {
			if ok {
				c.opts.KickSession(id)
			}
			c.selectClient("")
		}, c.win).Show()
}

// rowByID 按完整会话 ID 找当前快照行（找不到 = 已自行断开，返回 nil）。
func (c *Console) rowByID(id string) *ClientRow {
	for i := range c.clientRows {
		if c.clientRows[i].FullID == id {
			return &c.clientRows[i]
		}
	}
	return nil
}

// ---------- ③ 建议流 ----------

// sectionSuggestions 建议流面板：时间/来源/局面/着法/变招/分数/深度（第 0 行表头）。
// 数据是历史沉淀（联调/对局积累），不是实时流——标题与说明行明示语境，
// 避免未连接手机时误以为显示卡住。
func (c *Console) sectionSuggestions() fyne.CanvasObject {
	c.sugCount = &widget.Label{Text: "今日 0 条"}
	clearBtn := newHiliteButton("清空沉淀", c.confirmClearSuggestions)
	c.sugTable = widget.NewTable(
		func() (int, int) { return len(c.sugRows) + 1, 7 },
		func() fyne.CanvasObject {
			// 截断必须设在模板上：中文记法 4 字超列宽时防溢出压到相邻列
			return &widget.Label{Text: "占位文本宽度", Truncation: fyne.TextTruncateEllipsis}
		},
		func(id widget.TableCellID, o fyne.CanvasObject) {
			lbl := o.(*widget.Label)
			if id.Row == 0 {
				lbl.TextStyle = fyne.TextStyle{Bold: true}
				lbl.SizeName = theme.SizeNameCaptionText
				lbl.Importance = widget.LowImportance
				lbl.Alignment = fyne.TextAlignLeading
				lbl.SetText([...]string{"时间", "来源", "局面", "着法", "变招", "分数", "深度"}[id.Col])
				return
			}
			lbl.TextStyle = fyne.TextStyle{}
			lbl.SizeName = ""
			lbl.Importance = widget.MediumImportance
			lbl.Alignment = fyne.TextAlignLeading
			r := c.sugRows[id.Row-1]
			switch id.Col {
			case 0:
				lbl.TextStyle = fyne.TextStyle{Monospace: true}
				lbl.SetText(fmtTime(r.TS))
			case 1:
				lbl.SetText(fmtSource(r.Source))
			case 2:
				lbl.SetText(fmtFenBrief(r.FEN))
			case 3:
				// 设计稿 07：着法列中文纵线记法朱砂色（h2e2 → 炮二平五）；
				// 解析失败回退原始坐标白色等宽
				if cn := moveCN(r.FEN, r.BestMove); cn != "" {
					lbl.Importance = widget.HighImportance
					lbl.SetText(cn)
				} else {
					lbl.TextStyle = fyne.TextStyle{Monospace: true}
					lbl.SetText(r.BestMove)
				}
			case 4:
				// 变招列（复盘闭环 2026-10-17）：整条 PV 的中文记法顿号连接。
				// 桔库命中无 PV 显示"—"
				if len(r.PV) > 0 {
					lbl.Importance = widget.LowImportance
					lbl.SetText(pvCN(r.FEN, r.PV))
				} else {
					lbl.SetText("—")
				}
			case 5:
				lbl.TextStyle = fyne.TextStyle{Monospace: true}
				lbl.Alignment = fyne.TextAlignTrailing
				lbl.SetText(fmtScore(r.ScoreCP, r.ScoreMate))
			case 6:
				lbl.TextStyle = fyne.TextStyle{Monospace: true}
				lbl.Alignment = fyne.TextAlignTrailing
				if r.Depth > 0 {
					lbl.SetText(fmt.Sprintf("%d", r.Depth))
				} else {
					lbl.SetText("—")
				}
			}
		},
	)
	for i, w := range []float32{90, 130, 190, 100, 250, 90, 70} {
		c.sugTable.SetColumnWidth(i, w)
	}
	c.sugTable.SetRowHeight(-1, 30)
	// 可视区下限 120px（2026-10-30 用户定稿"盒砍一半"）+ 弹性吸收统一盒高
	// （advPanelH，2026-11-02）的剩余高——盒高恒定，表头 + 约四行起，其余盒内
	// 滚动；50 条数据全量可滚，盒只负责"统一"。尾部 separator 删除（盒内已有
	// 滚动边界，分隔线是冗余装饰）
	sugArea, _ := c.fixedTable(c.sugTable, 120)
	return container.New(&vboxFlex{gap: 4, flex: 2},
		sectionTitle("建议流（历史沉淀 · 最近 50 条）", c.sugCount, clearBtn),
		qiCaption("每次开局库命中 / 引擎出招沉淀一条；未连接手机时不会新增"),
		sugArea,
	)
}

// pvCN 整条 PV 的中文纵线记法（顿号连接，"变招"列）：从 fen 起逐步
// FenAfter 演算着法上下文再逐着 moveCN。演算失败（着法非法）时该步回退
// UCCI 原文并停止——顺序必须是"先演算后翻译"：moveCN 只查起点有子不查
// 合法性，非法着法会给出错误中文（如马走三格译成"马八进八"），不可信。
// 已译部分照常拼接，整条不报错（moveCN 的"纯展示转换"原则，只降级显示）。
func pvCN(fen string, pv []string) string {
	var parts []string
	cur := fen
	for _, mv := range pv {
		next, ok := xq.FenAfter(cur, mv)
		if !ok {
			parts = append(parts, mv)
			break // 演算断链：后续着法无上下文可译
		}
		if cn := moveCN(cur, mv); cn != "" {
			parts = append(parts, cn)
		} else {
			parts = append(parts, mv)
		}
		cur = next
	}
	return strings.Join(parts, "、")
}

// countToday 今日沉淀条数（选项缺省回 0）。
func (c *Console) countToday() int {
	if c.opts.CountToday == nil {
		return 0
	}
	return c.opts.CountToday()
}

// confirmClearSuggestions 清空沉淀的二次确认（防误触；确认后立即刷新）。
func (c *Console) confirmClearSuggestions() {
	n := len(c.sugRows)
	dialog.NewConfirm("清空沉淀",
		fmt.Sprintf("确认删除全部 %d 条历史建议？此操作不可恢复。", n),
		func(ok bool) {
			if !ok || c.opts.ClearSuggestions == nil {
				return
			}
			if err := c.opts.ClearSuggestions(); err != nil {
				dialog.NewError(err, c.win).Show()
				return
			}
			c.refresh()
		}, c.win).Show()
}

// ---------- ④ 库区 ----------

// sectionBooks 库管理面板（2026-11-01 便当盒重做，仿 03 连接：表头 + 卡片行
// 点选 + 操作区）：列表区弹性吸收统一盒高（advPanelH，2026-11-02）的剩余高，
// 库多时盒内滚动、盒高恒定（原"随行数收放、封顶"动态调高退役）；操作区按钮
// 作用于选中行（无选中禁用）。格式边界如实告知：仅支持 OBK（兵河格式），
// 库文件自备。
func (c *Console) sectionBooks() fyne.CanvasObject {
	c.bookRowsBox = container.New(&vbox{gap: 4})
	listArea, _ := c.fixedTable(c.bookRowsBox, 34)
	// 点击空白取消选中（tapCatcher 铺 Stack 底层，连接区同款）
	c.stackBooks = container.NewStack(newTapCatcher(func() {
		if c.selectedBook == "" {
			return
		}
		c.selectedBook = ""
		c.syncBookActionButtons()
		c.rebuildBookRows() // 复位选中描边
	}), listArea)

	rows := []fyne.CanvasObject{
		sectionTitle("开局库", bookHeaderRow()),
		c.stackBooks,
	}
	// 操作区（BookImportName==nil = 只读列表，操作区缺席）：导入/上移/下移/
	// 启停/删除/打开目录。上移/下移/启停/删除作用于选中行；导入与打开目录
	// 全局可用。格式边界声明不再单独占行（2026-11-02 用户定稿"小字全部进
	// info"）——收进行尾 ⓘ（书格式说明含授权/查询/导入规则全文）。
	if c.opts.BookImportName != nil {
		importBtn := newHiliteButton("导入库文件", func() { c.importBookDialog() })
		upBtn := widget.NewButton("上移", func() { c.moveSelectedBook(-1) })
		downBtn := widget.NewButton("下移", func() { c.moveSelectedBook(1) })
		tglBtn := widget.NewButton("禁用", func() { c.toggleSelectedBook() })
		// 删除：平时与普通按钮同形态，悬停才变红（2026-11-02 用户定稿
		// "红色太刺眼"——警示红由 dangerHoverBtn 的 tint 罩悬停时叠加，不走
		// Importance 切换：qiTheme 的 Hover 色不透明，悬停 blend 会把红全盖掉）
		delBtn := newDangerButton("删除", func() { c.removeSelectedBook() })
		openBtn := widget.NewButton("打开库目录", func() { openDir(c.opts.BooksDir) })
		c.bookUpBtn, c.bookDownBtn, c.bookTglBtn = upBtn, downBtn, tglBtn
		// &delBtn.Button 取字段地址（值嵌入下直接赋值会拷贝整个 Button 结构体
		// ——副本上 Enable/Disable 作用不到真实按钮）。悬停变红由包装层在
		// 同一结构上叠 tint 警示罩，与启停两路操作互不冲突。
		c.bookDelBtn = &delBtn.Button
		c.syncBookActionButtons()
		rows = append(rows, container.NewHBox(
			importBtn, upBtn, downBtn, tglBtn, delBtn, layout.NewSpacer(),
			newInfoChip("开局库格式说明",
				"本软件仅支持 OBK（兵河格式，SQLite 数据库）开局库文件，库文件一律自备，"+
					"请遵守原发布者授权条款并向原作者致谢（本软件不分发任何库文件）。\n\n"+
					"查询规则：按列表顺序逐库查询，首个命中即采用——\"上移/下移\"调整优先级，"+
					"\"启用/禁用\"决定该库是否参与查询。\n\n"+
					"导入：支持 .obk 与 .obk.zip（自动解压）；导入时先做 schema 预检，"+
					"结构不符的坏库会被拒绝并说明原因。目录内同名 .obk.zip 比旧文件新时自动解压覆盖。",
				c.win),
			openBtn,
		))
	}
	// 三个库策略开关一行（2026-11-02 用户定稿"压缩成一行"）：默认走云库 /
	// 残局库查询 / 不走任何库。括号注释与行尾小字全部并入各自 ⓘ（原三行
	// 各 ~28px + 说明行 ~20px 压成一行，面板收窄 ~80px）。语义：
	//   cloud_first 命中即终未命中回落（cloudLookupFirst）→ 残局独立补充
	//   （endgameLookupAsync）→ skip_books 全跳（handlePosition 最先拦截）。
	switchRow := container.NewHBox()
	if c.opts.CloudFirstGet != nil {
		c.cloudFirstChk = widget.NewCheck("默认走云库", func(on bool) {
			if c.cloudFirstBusy.Load() || c.opts.CloudFirstSet == nil {
				return
			}
			if msg := c.opts.CloudFirstSet(on); msg != "" {
				dialog.NewError(errors.New(msg), c.win).Show()
			}
		})
		switchRow.Add(c.cloudFirstChk)
		switchRow.Add(newInfoChip("关于\"默认走云库\"",
			"优先于本地库与引擎：开启后（仍需先勾选 02 区的\"云库查询\"）每步局面先查云库——"+
				"命中即采用云库建议，不再查本地开局库/沉淀、不进引擎（省算力正是收益，需要引擎深度分析时"+
				"用手机端\"更新\"或关掉本开关）；未命中或限流窗口内自动回落本地常规链，建议流不断供。\n\n"+
				"⚠隐私口径与\"云库查询\"一致：开启后对局局面 FEN 必然发往所配端点（默认 chessdb.cn）。",
			c.win))
	}
	if c.opts.EndgameGet != nil {
		c.endgameChk = widget.NewCheck("残局库查询", func(on bool) {
			if c.endgameBusy.Load() || c.opts.EndgameSet == nil {
				return
			}
			if msg := c.opts.EndgameSet(on); msg != "" {
				dialog.NewError(errors.New(msg), c.win).Show()
			}
		})
		switchRow.Add(c.endgameChk)
		switchRow.Add(newInfoChip("关于\"残局库查询\"",
			"云库穷举正解：开启后对本地开局库/沉淀都未命中的局面，另发一条残局库查询——查的是"+
				"云库（chessdb.cn）穷举计算的残局表，结论为绝对正解（胜/和/负与最优着法均经穷举验证）。\n\n"+
				"手机端展示：残局着法与引擎建议一致时，建议文案追加\"· 残局库\"；不一致时悬浮窗"+
				"并列两支箭头（琥珀色为残局库正解，绿色为当前建议）；无引擎建议时残局正解"+
				"直接作为主建议显示。\n\n"+
				"开关独立于\"云库查询\"，可只开残局；⚠隐私口径同云库：局面 FEN 会发往端点。",
			c.win))
	}
	if c.opts.SkipBooksGet != nil {
		c.skipBooksChk = widget.NewCheck("不走任何库", func(on bool) {
			if c.skipBooksBusy.Load() || c.opts.SkipBooksSet == nil {
				return
			}
			if msg := c.opts.SkipBooksSet(on); msg != "" {
				dialog.NewError(errors.New(msg), c.win).Show()
			}
		})
		switchRow.Add(c.skipBooksChk)
		switchRow.Add(newInfoChip("关于\"不走任何库\"",
			"每步引擎直搜：持续策略开关，开启后建议链跳过全部库——本地开局库、沉淀复用、"+
				"云库（含\"默认走云库\"）与残局库都不查。开启后无视云库开关（优先级最高）。\n\n"+
				"只停\"查\"不停\"记\"：引擎建议照常沉淀入库、建议流不断供；重新关闭后历史沉淀照常复用。",
			c.win))
	}
	switchRow.Add(layout.NewSpacer())
	rows = append(rows, switchRow)
	// flex:1 = stackBooks（rows[1]，恒定位置）：统一盒高下列表区弹性填充，
	// 操作区/开关行钉在底部
	return container.New(&vboxFlex{gap: 4, flex: 1}, rows...)
}

// importBookDialog 文件选择导入（全仓首个 FileOpen）：限定 .obk/.zip → 预检
// 拿目标名 → 同名 Confirm → 执行导入（拷进库目录，fsnotify 热加载自动接管）。
func (c *Console) importBookDialog() {
	fd := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.NewError(err, c.win).Show()
			return
		}
		if rc == nil {
			return // 用户取消
		}
		defer func() { _ = rc.Close() }()
		src := rc.URI().Path()
		target := c.opts.BookImportName(src)
		if target == "" {
			return // 预检失败原因已由实现侧记录日志；无明确反馈不弹空错
		}
		// 同名冲突先确认（覆盖用户库文件是破坏性动作）
		if _, err := os.Stat(filepath.Join(c.opts.BooksDir, target)); err == nil {
			dialog.NewConfirm("同名库已存在",
				fmt.Sprintf("库目录中已存在 %s，导入将覆盖它。继续？", target), func(ok bool) {
					if !ok {
						return
					}
					c.doImportBook(src)
				}, c.win).Show()
			return
		}
		c.doImportBook(src)
	}, c.win)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".obk", ".zip"}))
	fd.Resize(fyne.NewSize(640, 480))
	fd.Show()
}

// doImportBook 执行导入并反馈（Confirm 后段；预检失败/导入失败弹 Error）。
func (c *Console) doImportBook(src string) {
	if msg := c.opts.BookImportFile(src); msg != "" {
		dialog.NewError(errors.New(msg), c.win).Show()
		return
	}
	dialog.ShowInformation("导入完成", "库文件已放入库目录，热加载稍候自动生效。", c.win)
}

// moveSelectedBook 上移/下移选中库（delta -1/+1）；实现侧调整 order 并落盘。
func (c *Console) moveSelectedBook(delta int) {
	if c.selectedBook == "" || c.opts.BookMove == nil {
		return
	}
	if msg := c.opts.BookMove(c.selectedBook, delta); msg != "" {
		dialog.NewError(errors.New(msg), c.win).Show()
	}
}

// toggleSelectedBook 启用/禁用选中库；坏库行不可启停（不进查询链与开关无关）。
func (c *Console) toggleSelectedBook() {
	if c.selectedBook == "" || c.opts.BookToggle == nil {
		return
	}
	for _, b := range c.bookRows {
		if b.Name == c.selectedBook {
			if b.Status != "" {
				// 坏库点击：弹完整原因（加载失败与启停无关，先诊断）
				dialog.NewError(fmt.Errorf("库加载失败：%s", b.Status), c.win).Show()
				return
			}
			if msg := c.opts.BookToggle(b.Name, !b.Enabled); msg != "" {
				dialog.NewError(errors.New(msg), c.win).Show()
			}
			return
		}
	}
}

// removeSelectedBook 永久删除选中库：二次确认（明示"不进回收站"）→ 实现侧
// 先卸载连接再删 .obk 与同名附属（-wal/-shm/.obk.zip，见 library.RemoveBook）。
// 成功后立即整树刷新（签名比对重建列表，选中态随库消失一并复位）。
func (c *Console) removeSelectedBook() {
	name := c.selectedBook
	if name == "" || c.opts.BookRemove == nil {
		return
	}
	dialog.NewConfirm("删除库",
		fmt.Sprintf("确定永久删除库“%s”？\n\n将删除其库文件及同名附属文件"+
			"（-wal/-shm/.obk.zip），不进回收站，无法恢复。", name),
		func(ok bool) {
			if !ok {
				return
			}
			if msg := c.opts.BookRemove(name); msg != "" {
				dialog.NewError(errors.New(msg), c.win).Show()
				return
			}
			c.selectedBook = ""
			c.refresh()
		}, c.win).Show()
}

// syncBookActionButtons 操作区按钮可用性随选中态：无选中禁用上移/下移/启停/
// 删除；启停按钮文本随选中行状态（"禁用"/"启用"）。坏库行可删（删除是坏库
// 的正当出口之一，点击行弹完整原因由"原因"按钮承担）。
func (c *Console) syncBookActionButtons() {
	if c.bookUpBtn == nil {
		return
	}
	has := false
	for _, b := range c.bookRows {
		if b.Name == c.selectedBook {
			has = true
			if b.Status != "" {
				c.bookTglBtn.SetText("原因")
			} else if b.Enabled {
				c.bookTglBtn.SetText("禁用")
			} else {
				c.bookTglBtn.SetText("启用")
			}
			break
		}
	}
	if !has {
		c.bookTglBtn.SetText("禁用")
	}
	base := c.selectedBook != "" && has
	c.bookUpBtn.Disable()
	c.bookDownBtn.Disable()
	c.bookTglBtn.Disable()
	c.bookDelBtn.Disable()
	if base {
		c.bookUpBtn.Enable()
		c.bookDownBtn.Enable()
		c.bookTglBtn.Enable()
		c.bookDelBtn.Enable()
	}
}

// rebuildBookRows 全量重建库行（数据签名变化/选中复位时；库行少且变更罕见，
// 全量重建的抖动可忽略——签名比对在 refreshBooks）。坏库行的完整原因经操作区
// "原因"按钮查看（toggleSelectedBook 弹窗），点行只做选中。
func (c *Console) rebuildBookRows() {
	c.bookRowsBox.RemoveAll()
	for i, b := range c.bookRows {
		name := b.Name
		row := newBookRow(func(n string) {
			// 单选 toggle：再点同行取消选中
			if c.selectedBook == n {
				c.selectedBook = ""
			} else {
				c.selectedBook = n
			}
			c.syncBookActionButtons()
			c.rebuildBookRows()
		})
		row.set(i+1, b, c.selectedBook == name)
		c.bookRowsBox.Add(row)
	}
	c.syncBookActionButtons()
}

// ---------- ⑤ 日志尾部 ----------

// logTailLines 日志面板取尾部行数（紧凑渲染单行单事件，取 40 行留滚动余量；
// 2026-10-30 面板盒压到 ~140px 后可视约 7 行，40 行缓存全部靠盒内滚动看）。
const logTailLines = 40

// sectionLog 最近日志尾部（紧凑渲染：时间 + 级别着色 + 垃圾行剔除）+
// "打开日志目录"按钮；滚动跟随意图（贴底跟随、上翻不动，见 refresh）。
// 可视区下限 140（2026-10-30 用户定稿"盒砍一半"：约 7 行），弹性吸收统一盒高
// （advPanelH）剩余部分——盒高恒定，可视约 9 行起，其余盒内滚动。
func (c *Console) sectionLog() fyne.CanvasObject {
	c.logRich = widget.NewRichText()
	c.logRich.Wrapping = fyne.TextWrapWord
	scroll := container.NewScroll(c.logRich)
	c.logScroll = scroll
	openBtn := newHiliteButton("打开日志目录", func() { openDir(c.opts.LogDir) })
	return container.New(&vboxFlex{gap: 4, flex: 1},
		sectionTitle("日志", openBtn),
		container.New(&fixedHeight{h: 140}, scroll),
	)
}

// refreshLog 解析日志尾部并映射为级别着色的段落（每行独立段落，parse 失败的
// 异构行原样透传）；垃圾行已在 compactLogLines 剔除。签名短路（size+mtime）：
// 文件没动整段跳过——此前每 1.5s 无条件读文件+解析+重建 segments（sugSig/
// bookSig 都有防抖，此处 2026-11-02 审查补齐）。
func (c *Console) refreshLog() {
	var sig string
	if st, err := os.Stat(c.opts.LogFile); err == nil {
		sig = fmt.Sprintf("%d|%d", st.Size(), st.ModTime().UnixNano())
	} else {
		sig = "missing"
	}
	if sig == c.logSig {
		return
	}
	c.logSig = sig
	entries := compactLogLines(tailLines(c.opts.LogFile, logTailLines))
	if len(entries) == 0 {
		entries = []logEntry{{msg: "（暂无日志）"}}
	}
	segs := make([]widget.RichTextSegment, 0, len(entries))
	for _, e := range entries {
		segs = append(segs, &widget.TextSegment{
			Text: logLineText(e),
			Style: widget.RichTextStyle{
				TextStyle: fyne.TextStyle{Monospace: true},
				ColorName: logLevelColor(e.level),
			},
		})
	}
	c.logRich.Segments = segs
	c.logRich.Refresh()
}

// ---------- ⑥ 检查更新 ----------

// sectionUpdate 自动更新面板：检查（3s 超时静默）→ 确认式下载安装（进度/
// 取消）→ 自动重启换入（换入编排在 main，见 update 包）。文案全部由
// Checker 状态机生成，面板只做状态→控件的映射。
func (c *Console) sectionUpdate() fyne.CanvasObject {
	if c.opts.UpdateSnapshot == nil {
		return nil // main 未接线（no-tray 模式）：面板缺席，按钮自动隐藏
	}
	c.updateText = widget.NewLabel("尚未检查")
	// 结果文案自动换行：失败文案虽已分类短句化（update 包），仍留此硬兜底——
	// 任何超长串都不得撑破面板（2026-10-28 用户真机实录）。
	c.updateText.Wrapping = fyne.TextWrapWord
	c.updateBtn = newHiliteButton("立即检查", func() {
		if c.opts.UpdateCheck == nil {
			return
		}
		if msg := c.opts.UpdateCheck(); msg != "" {
			dialog.NewInformation("检查更新", msg, c.win).Show()
		}
	})
	// 确认式安装：点击即用户确认下载并替换本程序（绝不静默自动——拍板红线）
	c.updateDlBtn = newGlowButton("下载并安装", func() {
		if c.opts.UpdateDownloadInstall == nil {
			return
		}
		if msg := c.opts.UpdateDownloadInstall(); msg != "" {
			dialog.NewInformation("下载更新", msg, c.win).Show()
		}
	})
	c.updateDlBtn.Importance = widget.HighImportance
	c.updateDlBtn.Hide()
	c.updateCancelBtn = newHiliteButton("取消下载", func() {
		if c.opts.UpdateCancel != nil {
			c.opts.UpdateCancel()
		}
	})
	c.updateCancelBtn.Hide()
	c.updateBar = widget.NewProgressBar()
	c.updateBar.Hide()
	verLine := qiCaption("当前版本 " + c.opts.Version)
	c.updateAutoChk = widget.NewCheck("启动时自动检查（默认关）", func(on bool) {
		if c.opts.UpdateAutoCheckSet == nil {
			return
		}
		if msg := c.opts.UpdateAutoCheckSet(on); msg != "" {
			dialog.NewError(errors.New(msg), c.win).Show()
		}
	})
	if c.opts.UpdateAutoCheckGet != nil {
		c.updateAutoChk.SetChecked(c.opts.UpdateAutoCheckGet())
	}
	openBtn := newHiliteButton("打开 Release 页", c.openReleasePage)
	return container.NewVBox(
		sectionTitle("检查更新", openBtn),
		verLine,
		container.NewHBox(c.updateBtn, c.updateDlBtn, c.updateCancelBtn),
		c.updateBar,
		c.updateText,
		widget.NewSeparator(),
		c.updateAutoChk,
	)
}

// openReleasePage 浏览器打开最新稳定版 Release 页（只跳转不下载——拍板红线）。
// ReleaseURL 空（尚未检查到）时兜底到仓库 releases 列表页。
func (c *Console) openReleasePage() {
	target := ""
	if c.opts.UpdateSnapshot != nil {
		target = c.opts.UpdateSnapshot().ReleaseURL
	}
	if target == "" {
		target = update.DefaultReleasesURL
	}
	if u, err := url.Parse(target); err == nil {
		_ = c.app.OpenURL(u)
	}
}

// refreshUpdate 更新面板状态回填（refresh 循环调用；快照只读内存）。
func (c *Console) refreshUpdate() {
	if c.opts.UpdateSnapshot == nil || c.updateText == nil {
		return
	}
	s := c.opts.UpdateSnapshot()
	c.updateText.SetText(s.Message)
	// 检查按钮：流程进行中禁用（防重复触发；Checker 侧另有防重入兜底）
	busy := s.State == update.StateChecking || s.State == update.StateDownloading ||
		s.State == update.StateReadyReboot
	if busy {
		c.updateBtn.Disable()
	} else {
		c.updateBtn.Enable()
	}
	// 下载按钮：仅"发现新版本且资产带官方校验值"时出现
	if s.CanInstall {
		c.updateDlBtn.Show()
	} else {
		c.updateDlBtn.Hide()
	}
	// 进度条与取消：仅下载中
	if s.State == update.StateDownloading {
		c.updateBar.Show()
		c.updateBar.SetValue(s.Progress)
		c.updateCancelBtn.Show()
	} else {
		c.updateBar.Hide()
		c.updateCancelBtn.Hide()
	}
}

// fixedTable 内容固定可视高度（超出内部滚动）：widget.Table 的 MinSize 不随
// 行数增长，直接放 VBox 会塌缩或无限撑高，必须由外部容器锁定高度。
// v3 起也装自定义数据行列表（连接区）——参数泛化为 CanvasObject。
// 返回调高函数：连接区 v4 按"行数少随内容、多则封顶"动态收放（无设备时不
// 留大空白，设备多时封顶后内部滚动——两头都不违反"新连接不挤兑其他区块"）；
// 高级面板（建议流/日志/开局库）2026-11-02 起不再用 setter——列表区高度由
// vboxFlex 按统一盒高分配，setter 仅连接区保留。
func (c *Console) fixedTable(content fyne.CanvasObject, h float32) (fyne.CanvasObject, func(float32)) {
	f := &fixedHeight{h: h}
	box := container.New(f, container.NewScroll(content))
	return box, func(nh float32) {
		if f.h == nh {
			return
		}
		f.h = nh
		box.Refresh() // 高度变了必须重排（Layout 读 f.h）
	}
}

// fixedHeight 布局：宽度随父容器，高度下限锁死（h 只是最小可视高——父容器
// 给出更大高度时如实填满，vboxFlex 弹性分配统一盒高的前提；min 承诺仍为 h，
// "至少这么高、多了不嫌"）。
type fixedHeight struct{ h float32 }

func (f *fixedHeight) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	h := f.h
	if size.Height > h {
		h = size.Height
	}
	for _, o := range objs {
		o.Resize(fyne.NewSize(size.Width, h))
	}
}

func (f *fixedHeight) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(10, f.h)
}

// ---------- 刷新 ----------

// refresh 拉取全部快照并回填 UI（只在 fyne 主 goroutine 执行，fyne.Do 调入）。
func (c *Console) refresh() {
	if c.opts.Snapshot != nil {
		eng, clients := c.opts.Snapshot()
		set := func(k, v string) {
			if f, ok := c.engCells[k]; ok {
				f(v)
			}
		}
		setSuf := func(k, v string) {
			if f, ok := c.engSufCells[k]; ok {
				f(v)
			}
		}
		setLabel := func(k, v string) {
			if f, ok := c.engLabelCells[k]; ok {
				f(v)
			}
		}
		set("状态", fmtState(eng.State))
		// 深度仪表随思考模式切换语义（2026-10-29 定稿：时间制下不再显示旧
		// 深度目标——显示链全部跟随时间走）：时间制 = "用时 / 上限"（本轮已
		// 搜用时对时间上限），深度制 = "深度 / 目标"（实时层深对目标层深）
		const gaugeKey = "深度 / 目标"
		if eng.MoveTimeMS > 0 {
			setLabel(gaugeKey, "用时 / 上限")
			set(gaugeKey, fmtSearchElapsed(eng.SearchMS))
			setSuf(gaugeKey, " /"+tunerOptionText("movetime", eng.MoveTimeMS))
		} else {
			setLabel(gaugeKey, "深度 / 目标")
			set(gaugeKey, fmt.Sprintf("%d", eng.Depth))
			setSuf(gaugeKey, fmt.Sprintf("/%d", eng.TargetDepth))
		}
		set("节点", fmtCompactN(eng.Nodes))
		set("搜索速率", fmtNPS(eng.NPS))
		if eng.NPS > 0 {
			setSuf("搜索速率", "n/s")
		} else {
			setSuf("搜索速率", "")
		}
		set("哈希", fmtHashUsed(eng.Hashfull, eng.HashMB))
		// 配置行收敛为紧凑短式（bento 半列宽内不溢出；完整值在"引擎调参"面板）
		set("配置", fmt.Sprintf("线程 %s · 变招 %d", fmtThreads(eng.Threads), eng.MultiPV))
		// 模式行（2026-10-30 补空）：时间制给档位、深度制看"深度/目标"仪表
		if eng.MoveTimeMS > 0 {
			set("模式", "时间制 · "+tunerOptionText("movetime", eng.MoveTimeMS))
		} else {
			set("模式", "深度制")
		}
		c.clientRows = clients
		// 选中会话已消失（自行断开/被踢）时复位选中态，避免"断开会话"指向空 ID
		if c.selectedID != "" && c.rowByID(c.selectedID) == nil {
			c.selectedID = ""
			c.kickBtn.Disable()
		}
		// 卡片数量变化（设备上线/离线）才全量重建；其余周期逐卡回填不重建
		// （避免 1.5s 一次的视觉抖动）
		c.syncClientRows()
		c.connCountSet(fmt.Sprintf("%d 台", len(clients)))
	}
	c.refreshFirewall()
	c.refreshTuner()
	c.refreshUpdate()
	c.refreshTrend()
	if c.opts.Books != nil {
		books := c.opts.Books()
		c.bookRows = books
		c.updateBookTile(books)
		// 行数据签名（行数+每行名称/条数/状态/文件时间）：库集合变化是罕见事件
		// ——签名不变的刷新周期零重建（1.5s 防抖动），变了才全量重挂（选中态
		// 保留：选中库仍在列表中时 rebuild 后描边不丢）。ModTime 必须进签名：
		// 同名覆盖导入且条数恰好不变（修复版重打包）时，"文件时间"列要跟着
		// 热加载翻新——漏了它该行会永久显示旧时间（2026-11-02 审查修正）
		var sb strings.Builder
		for _, b := range books {
			fmt.Fprintf(&sb, "%s|%d|%t|%s|%d;", b.Name, b.Entries, b.Enabled, b.Status, b.ModTime.Unix())
		}
		if sig := fmt.Sprintf("%d|%s", len(books), sb.String()); sig != c.bookSig {
			c.bookSig = sig
			if c.bookRowsBox != nil {
				c.rebuildBookRows()
			}
			// 列表区高度由 vboxFlex 弹性分配（统一盒高，2026-11-02），多库盒内
			// 滚动——不再随行数调高
		}
	}
	c.refreshCloudBook()
	c.refreshCloudFirst()
	c.refreshEndgame()
	c.refreshSkipBooks()
	if c.opts.Recent != nil {
		rows := c.opts.Recent(50)
		// 签名短路：沉淀数据在绝大多数刷新周期无变化（1.5s 刷新节奏 vs 对局
		// 节奏），内容键相同则跳过行数据重赋值与表格 Refresh（后者触发全部
		// 可视 cell 重渲染）。签名 = 行数 + 首末行（时间,着法）：SQLite 侧
		// 时间文本秒级精度，补着法消"同秒多条"碰撞；清空沉淀行数骤变必穿透。
		// Recent 查询本身仍每轮执行（<1ms，取数才能判变没变；store 侧版本号
		// 属跨层加 API，收益不成比例）。
		sig := "0"
		if n := len(rows); n > 0 {
			sig = fmt.Sprintf("%d|%v|%s|%v|%s", n, rows[0].TS.UnixNano(), rows[0].BestMove,
				rows[n-1].TS.UnixNano(), rows[n-1].BestMove)
		}
		if sig != c.sugSig {
			c.sugSig = sig
			c.sugRows = rows
			c.sugTable.Refresh()
		}
		today := fmt.Sprintf("%d 条", c.countToday()) // 一次数出两处：面板 sugCount + 01 tile kv
		c.sugCount.SetText("今日 " + today)
		if f, ok := c.engCells["今日建议"]; ok {
			f(today)
		}
	}
	// 日志尾：紧凑渲染后按"跟随意图"滚动（2026-10-29 用户反馈"上划会原掉下来"
	// 的修复）：重建前判视口是否贴底，贴底（或刚展开面板的强制贴底）才拉到
	// 底部；用户上翻历史则保持偏移不动，滚回贴底即恢复自动跟随。阈值 24px
	// 吸收 RichText 重建前后的行高微差。
	wasAtBottom := false
	if c.logScroll != nil {
		contentH := c.logRich.MinSize().Height
		viewH := c.logScroll.Size().Height
		wasAtBottom = viewH > 0 && c.logScroll.Offset.Y >= contentH-viewH-24
	}
	c.refreshLog()
	if c.logScroll != nil && (c.logFollow || wasAtBottom) {
		contentH := c.logRich.MinSize().Height
		viewH := c.logScroll.Size().Height
		if contentH > viewH && viewH > 0 {
			c.logFollow = false
			c.logScroll.Offset = fyne.NewPos(0, contentH-viewH)
			c.logScroll.Refresh()
		}
	}
}

// orDash 空字符串显示占位符。
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// fmtTime 短时间（连接区/建议流）。
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("15:04:05")
}

// fmtDay 短日期（开局库 tile"最近更新"）。
func fmtDay(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("01-02")
}

// fmtSource 建议来源展示：book:桔库 → 开局库·桔库；engine → 引擎。
// 无需 cloud:/endgame: 分支：建议流数据源是沉淀库，而 recordSuggestion 只挂
// 桔库主选与引擎终态（见 server.position），云库/残局命中不落库——沉淀行
// source 恒为 "book:<名>"（复用行沿用原值）或 "engine"。
func fmtSource(s string) string {
	if strings.HasPrefix(s, "book:") {
		return "开局库·" + strings.TrimPrefix(s, "book:")
	}
	if s == "engine" {
		return "引擎"
	}
	return s
}

// fmtFullTime 带日期时间（库文件修改时间）。
func fmtFullTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("01-02 15:04:05")
}

// fmtFenBrief 建议流"局面"列的短展示：FEN 全长 60+ 字符放不进表格，
// 只取盘面段（第一段，含子力分布）+ 行棋方，超长截断加省略号；空值显示 —。
func fmtFenBrief(fen string) string {
	if fen == "" {
		return "—"
	}
	brief := fen
	if i := strings.IndexByte(fen, ' '); i > 0 && i+2 <= len(fen) {
		brief = fen[:i] + " " + fen[i+1:i+2] // 盘面段 + 行棋方（w/b）
	}
	const max = 28
	if len(brief) > max {
		brief = brief[:max-1] + "…"
	}
	return brief
}
