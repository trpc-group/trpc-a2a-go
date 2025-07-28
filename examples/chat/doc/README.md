# tRPC-A2A-Go 开发体验报告

## 一、体验场景构建

本次体验基于 tRPC-A2A-Go 框架实现了一个带 AI 机器人的多用户聊天室系统，该系统具备以下核心功能：

1. 多聊天室并发支持，每个聊天室有独立 ID
2. 用户身份管理，同一用户不能重复进入同一聊天室
3. 实时消息广播，所有用户可接收群内消息
4. AI 机器人交互，通过 "@ai" 前缀触发机器人回复
5. 支持流式消息推送，提升实时通信体验

## 二、开发环境与依赖

### 基础环境
- 操作系统：Ubuntu 24.04
- Go 版本：1.24+
- 依赖管理：Go Modules

### 核心依赖
- trpc.group/trpc-go/trpc-a2a-go v0.2.2：提供 A2A 协议实现
- github.com/tmc/langchaingo v0.1.13：AI 交互框架
- github.com/google/uuid v1.6.0：用户 ID 生成

## 三、开发过程体验

### 1. 项目结构搭建

按照 A2A 协议的客户端-服务端模型，设计了清晰的目录结构：

```
chat/
  ├── client/           # 聊天室客户端
  │   └── main.go
  └── server/           # 聊天室服务端
      └── main.go
  ├── go.mod            # 依赖管理
  └── README.md         # 使用说明
```

### 2. 服务端开发

服务端核心实现了：
- 聊天室管理（创建、用户加入/退出）
- 消息广播机制
- AI 机器人集成
- A2A 协议适配

关键代码片段解析：

```go
// 聊天室管理实现
type ChatRoomManager struct {
    rooms map[string]*ChatRoom
    mu    sync.Mutex
}

// 消息处理核心逻辑
func (p *chatMessageProcessor) ProcessMessage(
    ctx context.Context,
    message protocol.Message,
    options taskmanager.ProcessOptions,
    handler taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
    // 解析上下文获取房间ID和用户信息
    roomID, userID, userName := parseContext(message)
    
    // 获取或创建聊天室
    room := p.roomMgr.GetOrCreateRoom(roomID)
    
    // 添加用户到聊天室
    user := &User{ID: userID, Name: userName}
    room.AddUser(user)
    
    // 广播消息
    room.Broadcast(message)
    
    // 处理AI请求
    if strings.HasPrefix(text, "@ai") {
        // 异步调用AI并广播回复
        // ...
    }
}
```

### 3. 客户端开发

客户端主要实现：
- 连接服务端
- 消息发送
- 流式消息接收
- 用户输入处理

关键代码片段解析：

```go
// 发送消息实现
func sendMessage(ctx context.Context, a2aClient *client.A2AClient, roomID, userID, userName, msg string, streaming bool) {
    userMsg := protocol.NewMessage(
        protocol.MessageRoleUser,
        []protocol.Part{protocol.NewTextPart(msg)},
    )
    userMsg.ContextID = &roomID // 关联到聊天室
    userMsg.Metadata = map[string]interface{}{
        "user_id":   userID,
        "user_name": userName,
    }
    
    // 发送消息
    _, err := a2aClient.SendMessage(ctx, params)
    // ...
}

// 流式接收消息
func receiveStreaming(ctx context.Context, a2aClient *client.A2AClient, roomID, userID, userName string) {
    // 启动流式订阅
    eventChan, err := a2aClient.StreamMessage(ctx, params)
    // 循环接收消息
    for {
        select {
        case event, ok := <-eventChan:
            if m, ok := event.Result.(*protocol.Message); ok {
                printMessage(*m)
            }
        case <-ctx.Done():
            return
        }
    }
}
```

### 4. AI 集成

通过 langchaingo 框架集成 OpenAI API：

```go
func callAI(ctx context.Context, input string) (string, error) {
    llm, err := openai.New()
    if err != nil {
        return "", err
    }
    resp, err := llm.Call(ctx, input)
    return resp, err
}
```

## 四、功能测试与验证

### 1. 测试步骤

1. 安装依赖：`go mod tidy`
2. 配置环境变量：`export OPENAI_API_KEY=sk-xxxxxx`
3. 启动服务端：`cd server && go run .`
4. 启动多个客户端：
   ```bash
   cd client && go run . -room=room1 -user=张三
   cd client && go run . -room=room1 -user=李四
   ```

### 2. 功能验证

- **多用户聊天**：成功实现多用户消息实时广播
- **聊天室隔离**：不同房间消息互不干扰
- **AI 交互**：通过 "@ai 问题" 成功触发 AI 回复
- **用户唯一性**：同一用户无法重复加入同一房间
- **流式传输**：消息实时推送，无明显延迟

## 五、框架特性体验

### 1. tRPC-A2A-Go 优势

1. **协议封装完善**：提供了清晰的消息协议和交互模式，简化了客户端-服务端通信实现
2. **流式支持良好**：内置的流式消息机制简化了实时通信开发
3. **任务管理便捷**：TaskManager 组件简化了消息处理和状态管理
4. **扩展性强**：易于集成第三方服务（如本次集成的 OpenAI）

### 2. 可改进点

1. 文档不够丰富，部分 API 需要结合源码理解
2. 错误处理机制可以更完善
3. 缺少内置的身份认证机制，需要自行实现
4. 配置选项可以更灵活

## 六、总结与建议

tRPC-A2A-Go 框架为构建基于 A2A 协议的应用提供了良好的基础支持，尤其适合开发实时交互类应用。通过本次聊天室开发体验，框架的易用性和扩展性得到了验证。

建议：
1. 完善官方文档和示例
2. 增加更多场景化的示例代码
3. 提供更多内置中间件（如认证、日志等）
4. 优化错误提示和调试体验

总体而言，tRPC-A2A-Go 是一个有潜力的框架，适合用于构建分布式、实时交互的应用系统。