# Codex Exporter

一个轻量级的本地 Codex 对话导出工具，支持将 Codex 历史会话整理为清晰、易读的 Markdown 文件。

支持**单独导出完整会话**和**按项目批量导出**，自动展开长文本 Prompt，清理已识别的系统及 IDE 上下文，只保留真实用户输入和 Codex 的最终回答。

本项目基于 [faceair/codex-exporter](https://github.com/faceair/codex-exporter) 修改与扩展。

当前版本：**v1.2.0**

## 主要功能

### 1. 单独导出

从 Codex 历史会话列表中选择一个 Session，导出完整对话。

- 保留真实 User Prompt 和 Assistant 最终回答
- 保留原始对话顺序
- 自动展开支持的长文本粘贴附件
- 清理已识别的自动环境及 IDE 上下文
- 导出为易于阅读的 Markdown 文件

### 2. 按项目批量导出

自动识别本地 Codex 项目及其历史会话。

选择一个项目后，程序会批量导出该项目下所有符合条件的会话，**每个 Session 单独生成一个 Markdown 文件**。

不需要逐个选择会话，也不会将所有对话合并成一个巨型文件。

### 3. 其他功能

- 支持 `.jsonl` 与 `.jsonl.zst` 历史会话，包括 Codex 自动压缩的记录
- 支持中文会话标题与中文文件名
- 自定义默认保存目录
- 批量导出时自动识别并跳过已导出的会话
- 保护已有文件，避免意外覆盖
- 支持 Windows 便携版，无需安装 Go

## Windows 便携版

### 下载与启动

Windows 64 位便携版文件：

```
Codex-Exporter-v1.2.0-windows-amd64.zip
```

正式 GitHub Release 发布后，可在 Releases 页面获取。

解压 ZIP 后，双击 `Codex-Exporter.exe` 即可运行，无需安装 Go、Python 或其他解压工具。

程序提供中文终端菜单：

1. 单独导出一个会话
2. 按项目批量导出
3. 设置默认保存目录
4. 退出

使用方向键选择，按 Enter 确认，Esc 退出。

### 设置保存目录

首次使用时，可以自行指定 Markdown 保存位置。

支持两种方式：

- **便携目录**：按 `F2` 选择 EXE 所在文件夹下的 `exports` 目录
- **自定义目录**：输入其他磁盘或文件夹的完整路径

确认后程序会保存设置，下次启动时继续使用。

已经保存的目录不会被程序擅自更改。修改保存位置也不会自动移动或删除以前导出的文件。

### 查看导出结果

导出完成后，程序会显示文件保存位置及导出结果。

- 按 `O`：打开输出文件夹
- 按 Enter：返回主菜单
- 按 Esc：退出程序

批量导出还会显示成功、跳过及失败数量。

## 导出文件示例

导出的 Markdown 内容类似：

```
# 设计模板编辑器

# 🧑 User

请为当前项目新增模板编辑器，并保留已有功能。

---

# 💻 Assistant

已完成模板编辑器的实现……

---
```

默认文件名格式：

```
YYYYMMDD_会话标题.md
```

单独导出的文件保存在指定目录下，批量导出的文件按项目分别存放在子文件夹中。

## 重复导出与数据保护

批量导出会记录已成功导出的 Session，再次运行时默认跳过，避免重复生成大量文件。

- 不自动覆盖已有 Markdown
- 不会因为源会话新增内容就自动覆盖旧导出
- 同名会话会使用序号区分
- 单个会话导出失败，不影响其他可处理的会话

如果需要重新导出已经更新的会话，可以选择新的输出目录，生成一份新的快照。

## 命令行使用（可选）

除了交互式菜单，也可以通过命令行使用。

```
# 单独导出
.\Codex-Exporter.exe -single

# 按项目批量导出
.\Codex-Exporter.exe -batch

# 指定项目批量导出
.\Codex-Exporter.exe -batch -project "D:\Projects\Example"

# 修改默认保存目录
.\Codex-Exporter.exe -set-output-dir "D:\Codex-Exports"

# 本次使用指定保存目录
.\Codex-Exporter.exe -batch -output-dir "D:\Codex-Exports"

# 查看所有参数
.\Codex-Exporter.exe -help
```

高级参数包括 `-all`、`-last`、`-session-id`、`-output`、`-force`、`-full` 和 `-sessions-dir`，具体使用方法参见 `-help`。

其中 `-last` 表示最近一个 Session，不是最近 N 轮对话。

## 从源码运行

开发者需要安装符合 `go.mod` 要求的 Go 版本。

```
# 运行程序
go run .

# 执行测试
go test -count=1 ./...

# 静态检查
go vet ./...
```

Windows 下可使用现有构建脚本生成 EXE 与 ZIP：

```
.\scripts\build-windows.ps1
```

构建产物保存在 `dist/` 目录。

## 隐私与安全

本工具在本机读取和处理 Codex 会话，不会主动向外部 AI 服务上传对话内容。

- 只读访问 Codex 原始 Session 文件
- 不修改、删除或重新压缩原始历史会话
- 支持在内存中读取 Zstandard 压缩会话
- 仅在受信任的附件目录中展开已识别的长文本附件
- 不会将用户个人配置或历史会话打包进发布 EXE

**注意：导出的 Markdown 可能包含私人 Prompt、代码、项目路径和其他敏感信息。请自行妥善保管，不要直接提交到公开仓库。**

## 已知限制

- 目前提供 Windows 命令行交互界面，并非传统桌面 GUI
- 不提供 Clipboard、Last N Turns 或云端同步功能
- 部分未识别的旧版 Codex 自动上下文可能仍然保留
- 已丢失的附件无法仅凭会话记录恢复
- 超出读取限制或损坏的 Session 会报告错误
- 未来 Codex 存储格式发生变化时，可能需要更新兼容逻辑

## 来源与许可证

本项目基于 [faceair/codex-exporter](https://github.com/faceair/codex-exporter) 修改与扩展，感谢原作者的开源贡献。

本项目沿用 [MIT License](app://-/LICENSE)，保留原项目的版权声明。

第三方依赖及其许可证信息参见 [THIRD_PARTY_NOTICES.txt](app://-/THIRD_PARTY_NOTICES.txt)。

