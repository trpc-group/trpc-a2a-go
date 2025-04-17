# Redis Task Manager for A2A

This package provides a Redis-based implementation of the A2A TaskManager interface, allowing for persistent storage of tasks and messages using Redis.

## Features

- Persistent storage of tasks and task history
- Support for all TaskManager operations (send task, subscribe, cancel, etc.)
- Configurable key expiration time
- Compatible with Redis clusters, sentinel, and standalone configurations
- Thread-safe implementation
- Graceful cleanup of resources

## Requirements

- Go 1.21 or later
- Redis 6.0 or later (recommended)
- github.com/redis/go-redis/v9 library

## Installation

```bash
go get trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis
```

## Usage

### Basic Usage

```go
import (
    "context"
    "log"
    "time"

    "github.com/redis/go-redis/v9"
    "trpc.group/trpc-go/trpc-a2a-go/taskmanager"
    redismgr "trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis"
)

func main() {
    // Create your task processor implementation.
    processor := &MyTaskProcessor{}

    // Configure Redis connection.
    redisOptions := &redis.UniversalOptions{
        Addrs:    []string{"localhost:6379"},
        Password: "", // no password
        DB:       0,  // use default DB
    }

    // Create Redis task manager.
    manager, err := redismgr.NewRedisTaskManager(processor, redismgr.Options{
        RedisOptions: redisOptions,
    })
    if err != nil {
        log.Fatalf("Failed to create Redis task manager: %v", err)
    }
    defer manager.Close()

    // Use the task manager...
}
```

### Configuring Key Expiration

By default, task and message data in Redis will expire after 30 days. You can customize this:

```go
// Set custom expiration time.
expiration := 7 * 24 * time.Hour // 7 days

manager, err := redismgr.NewRedisTaskManager(processor, redismgr.Options{
    RedisOptions: redisOptions,
    Expiration:   &expiration,
})
```

### Using with Redis Cluster

```go
redisOptions := &redis.UniversalOptions{
    Addrs: []string{
        "redis-node-1:6379",
        "redis-node-2:6379",
        "redis-node-3:6379",
    },
    RouteByLatency: true,
}

manager, err := redismgr.NewRedisTaskManager(processor, redismgr.Options{
    RedisOptions: redisOptions,
})
```

### Using with Redis Sentinel

```go
redisOptions := &redis.UniversalOptions{
    Addrs:      []string{"sentinel-1:26379", "sentinel-2:26379"},
    MasterName: "mymaster",
}

manager, err := redismgr.NewRedisTaskManager(processor, redismgr.Options{
    RedisOptions: redisOptions,
})
```

## Implementation Details

### Redis Key Prefixes

The implementation uses the following key patterns in Redis:

- `task:ID` - Stores the serialized Task object
- `msg:ID` - Stores the message history as a Redis list
- `push:ID` - Stores push notification configuration

### Task Subscribers

While tasks and messages are stored in Redis, subscribers for streaming updates are maintained in memory. If your application requires distributed subscription handling, consider implementing a custom solution using Redis Pub/Sub.

## Testing

The package includes comprehensive tests that use an in-memory Redis server for testing. To run the tests:

```bash
go test -v
```

For end-to-end testing, the package uses [miniredis](https://github.com/alicebob/miniredis), which provides a fully featured in-memory Redis implementation perfect for testing without external dependencies.

## Full Example

See the [example directory](./example) for a complete working example.

## License

This package is part of the A2A Go implementation and follows the same license. 
