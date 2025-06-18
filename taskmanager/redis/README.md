# Redis TaskManager

Redis TaskManager 是 A2A TaskManager 接口的 Redis 实现，提供了基于 Redis 的消息、任务和会话管理功能。

## 特性

- **持久化存储**: 使用 Redis 持久化存储消息、任务和会话历史
- **分布式支持**: 支持多实例部署，通过 Redis 共享状态
- **高性能**: 利用 Redis 的高性能特性处理大量并发请求
- **灵活配置**: 支持自定义过期时间、历史长度等配置
- **接口兼容**: 完全实现 TaskManager 接口，与 MemoryTaskManager 兼容

## 安装

```go
import "trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis"
```

## 使用方法

### 1. 实现 RedisClient 接口

首先需要实现 `RedisClient` 接口，或者使用提供的适配器包装现有的 Redis 客户端：

```go
import (
    "github.com/redis/go-redis/v9"
    redisTaskManager "trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis"
)

// 创建 Redis 客户端
rdb := redis.NewClient(&redis.Options{
    Addr:     "localhost:6379",
    Password: "", // 如果有密码
    DB:       0,  // 使用默认数据库
})

// 包装为 RedisClient 接口
redisClient := redisTaskManager.NewRedisClientAdapter(rdb)
```

### 2. 创建 MessageProcessor

实现 `MessageProcessor` 接口来处理具体的业务逻辑：

```go
type MyMessageProcessor struct{}

func (p *MyMessageProcessor) ProcessMessage(
    ctx context.Context,
    message protocol.Message,
    options taskmanager.ProcessOptions,
    handle taskmanager.TaskHandle,
) (*taskmanager.MessageProcessingResult, error) {
    // 处理消息的业务逻辑
    // ...
    
    if options.Streaming {
        // 流式处理
        subscriber, err := handle.SubScribeTask(&taskID)
        if err != nil {
            return nil, err
        }
        
        // 异步处理任务
        go func() {
            defer subscriber.Close()
            // 处理逻辑...
        }()
        
        return &taskmanager.MessageProcessingResult{
            StreamingEvents: subscriber,
        }, nil
    } else {
        // 非流式处理
        result := &protocol.Message{
            Role: protocol.MessageRoleAgent,
            Parts: []protocol.Part{
                protocol.NewTextPart("处理完成"),
            },
        }
        
        return &taskmanager.MessageProcessingResult{
            Result: result,
        }, nil
    }
}
```

### 3. 创建 Redis TaskManager

```go
// 创建 Redis TaskManager
taskManager, err := redis.NewRedisTaskManager(
    redisClient,
    &MyMessageProcessor{},
    redis.WithExpiration(24*time.Hour),     // 设置过期时间
    redis.WithMaxHistoryLength(200),        // 设置最大历史长度
)
if err != nil {
    log.Fatal(err)
}
defer taskManager.Close()
```

### 4. 使用 TaskManager

```go
// 发送消息
result, err := taskManager.OnSendMessage(ctx, protocol.SendMessageParams{
    Message: protocol.Message{
        MessageID: "msg-123",
        ContextID: &contextID,
        Role:      protocol.MessageRoleUser,
        Parts: []protocol.Part{
            protocol.NewTextPart("Hello, world!"),
        },
    },
})

// 流式处理
eventChan, err := taskManager.OnSendMessageStream(ctx, protocol.SendMessageParams{
    Message: message,
})
if err != nil {
    log.Fatal(err)
}

// 监听事件
for event := range eventChan {
    switch result := event.Result.(type) {
    case *protocol.Message:
        fmt.Printf("收到消息: %v\n", result)
    case *protocol.TaskStatusUpdateEvent:
        fmt.Printf("任务状态更新: %s -> %s\n", result.TaskID, result.Status.State)
    case *protocol.TaskArtifactUpdateEvent:
        fmt.Printf("收到工件: %s\n", result.Artifact.ArtifactID)
    }
}
```

## 配置选项

### WithExpiration

设置 Redis 键的过期时间：

```go
redis.WithExpiration(7 * 24 * time.Hour) // 7天过期
```

### WithMaxHistoryLength

设置每个会话的最大历史消息数量：

```go
redis.WithMaxHistoryLength(500) // 最多保存500条历史消息
```

## Redis 键结构

Redis TaskManager 使用以下键前缀：

- `msg:` - 存储消息内容
- `conv:` - 存储会话历史（消息ID列表）
- `task:` - 存储任务信息
- `push:` - 存储推送通知配置

## 性能考虑

1. **连接池**: 确保 Redis 客户端使用连接池来处理并发请求
2. **内存使用**: 定期清理过期的键，避免内存泄漏
3. **网络延迟**: 考虑 Redis 服务器的网络延迟对性能的影响
4. **数据序列化**: 使用高效的序列化方式（当前使用 JSON）

## 故障处理

1. **Redis 连接失败**: TaskManager 会在创建时测试连接，确保 Redis 可用
2. **数据丢失**: 考虑使用 Redis 持久化（RDB/AOF）来防止数据丢失
3. **网络分区**: 实现适当的重试和降级策略

## 示例

完整的使用示例请参考 `example_redis_client.go` 文件。 