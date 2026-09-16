# taskhub

在本机、远端 Mac 和服务器之间共享任务。一个可执行文件同时提供 CLI 和 HTTP 服务。每个任务包含标题、Markdown 正文、状态、可选的项目名 `project` 和自动生成的 ID。

[English](README.md) · [下载](https://github.com/felixfeng33/taskhub/releases)

## 安装

从 Releases 下载对应系统和架构的压缩包，核对 `checksums.txt`，解压后把 `taskhub` 放到 PATH。支持 macOS / Linux、ARM64 / AMD64，无需安装 Go、Node.js 或 SQLite。

也可以先下载并查看安装脚本，再运行：

```sh
curl -fsSL https://raw.githubusercontent.com/felixfeng33/taskhub/main/scripts/install.sh -o install-taskhub.sh
sh install-taskhub.sh v0.2.0
export PATH="$HOME/.local/bin:$PATH"
taskhub version
```

安装脚本会核对 SHA-256。默认安装到 `~/.local/bin`，可通过 `INSTALL_DIR` 指定目录。以后重新运行脚本安装新版本即可，配置和任务数据单独保存。

## 服务器

```sh
mkdir -p taskhub-data
chmod 700 taskhub-data
umask 077
openssl rand -hex 32 > taskhub-data/token
taskhub serve --db taskhub-data/tasks.db --token-file taskhub-data/token
```

默认监听 `127.0.0.1:8080`。公网使用 HTTPS 反向代理，或通过 SSH 隧道访问；私有网络可用 `--listen` 指定监听地址。程序自身提供 HTTP，不处理 TLS。也可使用 `TASKHUB_TOKEN` 环境变量替代 token 文件。

## 两台 Mac

将同一个 token 安全地放到每台设备，然后配置服务器地址：

```sh
taskhub config --url https://tasks.example.com --token-stdin < taskhub-data/token
```

本机测试可用 `http://127.0.0.1:8080`。macOS 配置保存在 `~/Library/Application Support/taskhub/config.json`，文件权限为 `0600`。可以用 `--config PATH` 指定其他配置文件，或用 `TASKHUB_URL`、`TASKHUB_TOKEN` 临时覆盖配置。

## 使用

```sh
# 本机定稿后新增任务
taskhub add --title "实现登录" --project ellie --body-file spec.md

# 远端读取任务
taskhub list --project ellie --status pending --json
taskhub show 1 --body-only

# 原子领取，避免两个执行端同时拿到同一个待执行任务
taskhub update 1 --status in_progress --if-status pending

# 修改标题或完整正文
taskhub update 1 --title "实现邮箱登录"
taskhub update 1 --project ellie
taskhub update 1 --body-file updated-spec.md

# 远端完成，等待验收
taskhub update 1 --status review

# 本机验收通过
taskhub update 1 --status done
```

状态对应：`pending` 待执行、`in_progress` 执行中、`review` 待验收、`done` 已完成。验收不通过可以重新设为 `pending`。

`add / list / show / update / archive / unarchive` 都支持 `--json`。ID 放在 `show / update / archive / unarchive` 后面，参数放在 ID 后面。`--body-file -` 从标准输入读取正文；`--body ''` 清空正文；没有提供的字段保持原值。

领取冲突时退出码为 `3`，其他错误为 `1`，成功为 `0`。中断的任务保持执行中，需要手动更新状态。需求、验收标准、反馈和最终 Git 提交号都写在正文里。正文修改为整篇替换，同一任务同时编辑时以最后一次成功写入为准。

工具负责保存和读写任务；启动 Codex、执行 Git、判断是否完成，交给调用它的人或 agent。没有后台自动接单、用户权限分组或文档版本功能。

HTTP API、分页、Linux systemd 服务、HTTPS 配置与数据库备份方法见 [English README](README.md)。服务端所有任务接口都需要 Bearer token，`/healthz` 只公开健康状态和版本。

## 项目分类

一个任务最多关联一个项目。名称直接填写即可使用，区分大小写，去掉首尾空白，最多 80 个 Unicode 字符，不允许控制字符。

```sh
taskhub add --title "修复编辑器" --project plate --body-file spec.md
taskhub list --project plate --status pending
taskhub update 1 --project ellie
taskhub update 1 --project ''   # 清除项目关联
taskhub list --project ''       # 只看未关联项目的任务
taskhub list                   # 查看全部项目
```

旧任务默认没有项目，JSON 中为 `"project":""`。修改其他字段时不传 `--project`，就会保留原来的项目。项目名用于筛选任务，不会自动选择代码目录或启动 agent。

## 归档与恢复

```sh
taskhub archive 1                     # 归档
taskhub list                          # 默认不显示归档任务
taskhub show 1                        # 归档后返回 task not found
taskhub list --archived --project ellie
taskhub show 1 --archived              # 显式查看归档任务
taskhub unarchive 1                   # 恢复
```

归档不会删除任务，也不会改变原来的标题、正文、项目或状态。`archived` 是独立标记。默认 `list` 和 `show` 在服务器端隐藏归档任务，旧客户端也一样。`list --archived` 只列出归档任务，`show --archived` 允许读取归档任务。先恢复任务才能继续修改内容。

HTTP API 使用 `PATCH /tasks/{id}`，正文为 `{"archived":true}` 或 `{"archived":false}`。归档操作和内容编辑分开提交。

## 从 v0.1.0 升级

先升级服务器，再升级客户端。服务器启动时会在事务中自动补上 `project` 和 `archived` 字段，保留已有任务的 ID、标题、正文和状态，旧任务默认未归档。重复启动不会重置任务。更新已有部署前先备份数据库。

v0.1.0 客户端仍能读取和修改任务，修改时会保留已有的项目。要使用项目分类和归档命令，需要将客户端升级到 v0.2.0 或更新版本。

## 开发

```sh
go test -race ./...
go vet ./...
go build -o bin/taskhub ./cmd/taskhub
```

MIT 开源协议。发布 `v*` 标签后，GitHub Actions 自动构建四种平台安装包并发布到 Releases。
