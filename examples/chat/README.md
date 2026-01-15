## 背景

基于 trpc-a2a-go 和 langchaingo 实现一个带 AI 机器人的简单聊天室。

## 环境

- OS: Ubuntu 24.04
- Go: 1.24 及以上
- 依赖：trpc-a2a-go、langchaingo、OpenAI API Key（如需 AI 机器人）

## 功能细节

1. 每个聊天室有独立 ID，每个用户有独立 ID，一个用户不能重复进入同一个聊天室。
2. 有人发消息会广播至所有人（包括自己）。
3. 消息以 "@ai" 开头，则触发机器人回复。
4. AI 机器人回复基于 langchaingo（OpenAI）。

## 目录结构

```
examples/chat/
  ├── client/
  │   └── main.go   # 聊天室命令行客户端
  └── server/
      └── main.go   # 聊天室服务端（含 AI 机器人）
```

## 运行方式

### 1. 安装依赖

```shell
cd examples/chat
# 安装 trpc-a2a-go 及 langchaingo 依赖
# (如未安装)
go mod tidy
```

### 2. 配置 OpenAI API Key（如需 AI 机器人）

```shell
export OPENAI_API_KEY=sk-xxxxxx
```

### 3. 启动服务端

```shell
cd server
# 默认监听 8080 端口
go run .
```

### 4. 启动客户端

```shell
cd ../client
# -room 指定聊天室ID，-user 指定用户名
go run . -room=room1 -user=张三
# 可多开终端模拟多用户
go run . -room=room1 -user=李四
```

### 5. 聊天体验

- 输入内容直接发送消息。
- 输入 `@ai 你好` 可触发 AI 机器人回复。
- 所有用户实时收到群聊消息。

## 备注

- 支持流式消息推送。
- 支持多聊天室并发。
- 如需自定义端口、服务地址等，可用 `-host`、`-port` 参数。
- 代码结构清晰，便于二次开发。

---

如遇问题可参考 `examples/chat/server/main.go` 和 `examples/chat/client/main.go`，或 issue 反馈。
