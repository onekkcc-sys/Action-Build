# Agent Hub · 账号管家

管理多个 AI Agent 账号（Claude / Codex / 其他）的 Windows 小工具：看每个账号 **5 小时窗口** 和 **每周额度** 还剩多少、什么时候重置，给账号配一个 Agent 名字和独立配置目录，一键用对应账号启动 `claude` / `codex`。

双击 `AgentHub.exe` 即可，不需要安装。界面在一个独立的 Edge 应用窗口里打开（系统没有 Edge/Chrome 时用默认浏览器），关掉窗口程序就退出。

## 功能

- **用量与额度**：每个账号一张卡片
  - 5 小时 / 每周剩余百分比进度条、重置时间、实时倒计时
  - 重置时间已过 → 显示「重置前剩 X%」和「可能已恢复（未验证）」
  - 读数超过 N 小时（默认 6）→ 标「旧」，进度条变灰；低于 15% 变色
  - 套餐没有 5 小时窗口（如部分 Pro 套餐）→ 显示「这个套餐现在没有 5 小时窗口」
  - 顶部给出「建议下一个用」的账号，可按类型筛选、按剩余/重置时间排序
- **账号列表**（左侧）：Agent 名字 · 账号名、在线 / 离线·可先留言 / 还没接入 / 运行中，右侧小字是当前可用额度
- **账号详情**：启动按钮（新开终端，自动设置 `CLAUDE_CONFIG_DIR` 或 `CODEX_HOME`，多账号互不干扰）、打开配置目录、留言、关联的待拍板事项
- **待拍板**：记下需要你拍板的事，可关联账号
- **数据来源**
  - 手动录入：重置时间支持 `2h30m`、`2小时30分`、`09:00`、`明天 9:00`、`10-04 09:00`，也可以填「已用 %」
  - Codex 自动读取：开启后定时读取 `CODEX_HOME/sessions/**/*.jsonl` 里的额度（卡片显示「会话文件」）
  - 会话上报 API：脚本推送读数（卡片显示「会话上报」），见下
- 搜索（Ctrl+K）、导出/导入 JSON 备份

## 会话上报 API

只监听 `127.0.0.1:17321`。Token 在「设置」里查看。

```bash
curl -X POST http://127.0.0.1:17321/api/report \
  -H "X-Token: <token>" -H "Content-Type: application/json" \
  -d '{"account":"Claude 1","five_hour":{"left":77,"resets_in_seconds":9000},"weekly":{"used":32,"reset":"2026-10-09T15:00:00+08:00"}}'
```

- `account`：账号名称、Agent 名字或 ID
- 每个窗口可用 `left` / `remaining`（剩余 %）或 `used` / `used_percent` / `utilization`（已用 %）
- 重置时间可用 `reset` / `resets_at`（ISO 时间或 unix 秒）或 `resets_in_seconds`
- 也直接接受 Claude 风格的 `five_hour` / `seven_day`，以及 Codex 风格的 `rate_limits.primary/secondary`（带 `window_minutes`）
- `"no_five_hour": true` 表示套餐没有 5 小时窗口

## 数据位置

`%APPDATA%\AgentHub\data.json`。把 `data.json` 放到 exe 同目录就变成便携版（数据跟着 exe 走）。

## 构建

需要 Go 1.22+，在任意系统上交叉编译：

```bash
./build.sh            # 输出 dist/AgentHub.exe
```

推送 `agent-hub/` 下的改动时，GitHub Actions（`.github/workflows/agent-hub.yml`）也会自动构建并上传 exe。

图标资源 `rsrc_windows_amd64.syso` 由 `python3 tools/make_icon.py` 生成（需要 Pillow），已提交，一般不用重新生成。

命令行参数：`--port 17321`、`--data <目录>`、`--no-window`（只起服务）。
