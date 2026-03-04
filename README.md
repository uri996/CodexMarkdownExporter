# codex-exporter

将 Codex 的 session rollout（`rollout-*.jsonl`）导出为 Markdown。

## 功能概览

- 从 Codex sessions 目录扫描可导出的会话。
- 默认仅显示并导出当前工作目录（`cwd`）对应的会话；可用 `-all` 关闭该过滤。
- 默认进入交互式会话选择器；可用 `-last` 或 `-session-id` 跳过选择器。
- 默认仅导出用户消息和助手最终回复（`final` / `final_answer`）。
- 使用 `-full` 可导出完整对话（包含助手非最终 phase），并在 Markdown 顶部附带完整元数据。
- 默认输出文件名为 `./codex-session-<session-id>.md`；已存在时需加 `-force` 才会覆盖。

## 环境要求

- Go `1.24.2` 或更高版本（以 `go.mod` 为准）。

## 安装与运行

### 方式 1：直接运行

```bash
go run .
```

### 方式 2：构建后运行

```bash
go build -o codex-exporter .
./codex-exporter
```

## 参数说明

以下参数以 `go run . -h` 的实际输出为准：

| 参数 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-all` | bool | `false` | 显示所有会话（关闭当前目录过滤） |
| `-force` | bool | `false` | 覆盖已存在的输出文件 |
| `-full` | bool | `false` | 导出完整对话（包含助手非 final phase） |
| `-last` | bool | `false` | 直接导出最新会话，不进入选择器 |
| `-output` | string | 空 | 指定输出 Markdown 文件路径 |
| `-session-id` | string | 空 | 按会话 ID 直接导出，不进入选择器 |
| `-sessions-dir` | string | `~/.codex/sessions` | Codex sessions 目录 |

## 使用示例

```bash
# 交互选择当前目录下的会话并导出
go run .

# 显示所有目录的会话再选择
go run . -all

# 直接导出最新会话
go run . -last

# 指定会话 ID，导出到固定文件并允许覆盖
go run . -session-id 11111111-1111-1111-1111-111111111111 -output ./session.md -force

# 导出完整对话（含非 final phase）
go run . -last -full
```

## 测试

```bash
go test ./...
```

