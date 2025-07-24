# tRPC-A2A-Go 聊天室体验报告

## 一、体验背景

本次体验基于 tRPC-A2A-Go 框架实现一个简单的聊天室功能，旨在熟悉 tRPC 框架的双向流式通信能力及 A2A（Application-to-Application）通信规范。通过实践了解 tRPC 在构建实时通信应用场景下的开发流程、接口设计及部署方式。

## 二、环境准备

1. **基础环境**
    - 操作系统：Ubuntu 24.04
    - Go 版本：1.24
    - tRPC 相关工具：trpc-cli

2. **依赖安装**
   ```shell
   # 安装 tRPC 代码生成工具
   go install trpc.group/trpc-go/trpc-cmdline/trpc@latest
   ```

## 三、开发过程

### 1. 协议设计（Protobuf）

根据聊天室场景需求，定义了 `ChatroomMessage` 消息结构和 `ChatroomService` 服务接口，使用 proto3 语法：

```protobuf
syntax = "proto3";

package chat;
option go_package = "chat/protobuf;chat";

message ChatroomMessage {
  string room_id = 1; // 聊天室的唯一标识
  string sender = 2;  // 消息发送者
  string content = 3; // 消息内容
}

service ChatroomService {
  // 双向流式的聊天室通信方法
  rpc Chat(stream ChatroomMessage) returns (stream ChatroomMessage);
}
```

关键设计说明：
- 采用双向流式 RPC（`stream`）实现实时消息推送
- 消息包含聊天室 ID、发送者和内容三个核心字段
- 通过 `go_package` 指定生成代码的 Go 包路径

### 2. 代码生成

使用 tRPC 工具根据 proto 文件生成基础代码：

```makefile
.PHONY: chat-proto
chat-proto:
	trpc create -p protobuf/chat.proto -o out
```

执行生成命令：
```shell
make chat-proto
```

生成结果：在 `out` 目录下自动创建了服务端和客户端的基础框架代码，包括：
- 服务接口定义
- 消息编解码逻辑
- 网络传输层封装

### 3. 业务逻辑实现

#### 服务端实现
服务端需要维护聊天室会话，实现消息转发逻辑：
- 维护房间与连接的映射关系
- 接收客户端消息并广播给同房间其他用户
- 处理新用户加入和用户离开事件

#### 客户端实现
客户端实现用户交互和消息收发：
- 命令行参数解析（用户名、房间号、服务端地址）
- 从标准输入读取用户消息并发送
- 监听服务端推送的消息并显示

### 4. 运行与测试

1. 启动服务端
```shell
cd example/chat/out
go run .
```

2. 启动多个客户端（新终端窗口）
```shell
# 客户端1
go run cmd/client/main.go --name="Alice" --room="anonymous" --target="ip://127.0.0.1:8000"

# 客户端2
go run cmd/client/main.go --name="Bob" --room="anonymous" --target="ip://127.0.0.1:8000"
```

3. 功能测试
- 发送消息：在任一客户端输入文本并回车，其他客户端可收到消息
- 多房间隔离：不同房间的消息不会互相干扰
- 身份标识：消息会显示发送者名称，区分不同用户

## 四、A2A 特性体验

1. **双向流式通信**
   通过 tRPC 的流式 RPC 实现了全双工通信，客户端和服务端可随时发送消息，满足聊天室实时交互需求。

2. **服务发现与注册**
   体验了 tRPC 内置的服务发现机制，客户端通过 `target` 参数指定服务端地址即可建立连接。

3. **协议兼容性**
   基于 Protobuf 协议实现，具备良好的跨语言兼容性，可扩展支持多语言客户端接入。

4. **错误处理**
   框架自动处理网络异常和连接断开情况，客户端断开后服务端会自动清理会话。

## 五、问题与解决方案

1. **消息广播效率**
   问题：当房间用户较多时，消息广播可能出现延迟
   解决方案：实现消息异步转发，使用缓冲通道减少阻塞

2. **客户端重连**
   问题：网络中断后客户端需要手动重启
   解决方案：在客户端实现自动重连机制，检测连接状态并尝试重新连接

## 六、总结与展望

### 体验总结
1. tRPC-A2A-Go 框架提供了简洁的接口定义和代码生成能力，大幅降低了实时通信应用的开发门槛
2. 双向流式 RPC 机制非常适合聊天室这类需要持续交互的场景
3. 完善的工具链支持（代码生成、构建部署）简化了开发流程

### 未来优化方向
1. 增加用户认证机制，确保消息发送者身份合法性
2. 实现消息持久化，支持历史消息查询
3. 扩展支持文件传输、表情等富媒体消息
4. 优化服务端架构，支持水平扩展以应对高并发场景

通过本次体验，深入了解了 tRPC-A2A-Go 在实时通信场景下的应用方式，框架的设计理念和易用性给人留下深刻印象，适合用于构建高性能的分布式应用。