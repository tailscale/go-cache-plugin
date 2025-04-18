module github.com/tailscale/go-cache-plugin

go 1.24.0

toolchain go1.24.2

require (
	github.com/aws/aws-sdk-go-v2/config v1.29.5
	github.com/aws/aws-sdk-go-v2/service/s3 v1.75.3
	github.com/creachadair/atomicfile v0.3.7
	github.com/creachadair/command v0.1.20
	github.com/creachadair/flax v0.0.4
	github.com/creachadair/gocache v0.0.0-20250108235800-51cd8478f1c9
	github.com/creachadair/mds v0.23.0
	github.com/creachadair/mhttp v0.0.0-20241114003125-97da0a4f17b1
	github.com/creachadair/scheddle v0.0.0-20241121045015-b2e30c9594a1
	github.com/creachadair/taskgroup v0.13.2
	github.com/creachadair/tlsutil v0.0.0-20241111194928-a9f540254538
	github.com/goproxy/goproxy v0.18.0
	golang.org/x/sync v0.13.0
	golang.org/x/sys v0.32.0
	honnef.co/go/tools v0.6.1
	tailscale.com v1.82.5
)

require (
	github.com/BurntSushi/toml v1.4.1-0.20240526193622-a339e1f7089c // indirect
	github.com/aws/aws-sdk-go-v2 v1.36.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.6.8 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.17.58 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.16.27 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.3.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.6.31 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.3.31 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.12.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.5.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.12.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.18.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.24.14 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.28.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.33.13 // indirect
	github.com/aws/smithy-go v1.22.2 // indirect
	github.com/creachadair/msync v0.4.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20250223041408-d3c622f1b874 // indirect
	go4.org/mem v0.0.0-20240501181205-ae6ca9944745 // indirect
	go4.org/netipx v0.0.0-20231129151722-fdeea329fbba // indirect
	golang.org/x/crypto v0.35.0 // indirect
	golang.org/x/exp/typeparams v0.0.0-20240314144324-c7f7c6466f7f // indirect
	golang.org/x/mod v0.23.0 // indirect
	golang.org/x/net v0.36.0 // indirect
	golang.org/x/tools v0.30.0 // indirect
)

retract (
	v0.0.19
	v0.0.16
)

tool honnef.co/go/tools/cmd/staticcheck
