module trpc.group/trpc-go/trpc-a2a-go/examples

go 1.23.0

toolchain go1.23.7

require (
	github.com/google/uuid v1.6.0
	github.com/lestrrat-go/jwx/v2 v2.1.4
	github.com/redis/go-redis/v9 v9.10.0
	golang.org/x/oauth2 v0.29.0
	trpc.group/trpc-go/trpc-a2a-go v0.0.3
	trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis v0.0.0-20250625115112-3bb198d0dc98
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/goccy/go-json v0.10.3 // indirect
	github.com/golang-jwt/jwt/v5 v5.2.2 // indirect
	github.com/lestrrat-go/blackmagic v1.0.2 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/httprc v1.0.6 // indirect
	github.com/lestrrat-go/iter v1.0.2 // indirect
	github.com/lestrrat-go/option v1.0.1 // indirect
	github.com/segmentio/asm v1.2.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	golang.org/x/crypto v0.35.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
)

replace trpc.group/trpc-go/trpc-a2a-go => ../

replace trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis => ../taskmanager/redis
