## 背景
基于trpc实现一个简单的聊天室

## 环境
- os ubuntu24.04
- go 1.24

## 运行

```shell
  cd example/chat/out
```

1. 启动服务端
```shell
  go run .
```
2. 启动客户端
```shell
  go run cmd/client/main.go --name="default" --room="anonymous" --target="ip://127.0.0.1:8000""
```


