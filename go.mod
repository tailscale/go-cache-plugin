module github.com/tailscale/go-cache-plugin

go 1.26.1

require (
	github.com/aws/aws-sdk-go-v2 v1.41.0
	github.com/aws/aws-sdk-go-v2/config v1.31.5
	github.com/aws/aws-sdk-go-v2/service/s3 v1.87.2
	github.com/creachadair/atomicfile v0.3.8
	github.com/creachadair/command v0.2.0
	github.com/creachadair/flax v0.0.5
	github.com/creachadair/gocache v0.0.0-20250825180902-ad2fcf0fe74b
	github.com/creachadair/mds v0.25.9
	github.com/creachadair/mhttp v0.0.0-20250816170017-6ba77f126e85
	github.com/creachadair/scheddle v0.0.0-20250829170737-bd8143d4c547
	github.com/creachadair/taskgroup v0.14.0
	github.com/creachadair/tlsutil v0.0.0-20250624153316-15acc082fa38
	github.com/goproxy/goproxy v0.21.0
	golang.org/x/sync v0.19.0
	golang.org/x/sys v0.40.0
	honnef.co/go/tools v0.7.0-0.dev.0.20251022135355-8273271481d0
	tailscale.com v1.96.5
)

require (
	github.com/BurntSushi/toml v1.5.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.1 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.18.9 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.5 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.16 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.16 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.3 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.8.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.16 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.29.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.34.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.41.5 // indirect
	github.com/aws/smithy-go v1.24.0 // indirect
	github.com/creachadair/msync v0.7.1 // indirect
	github.com/go-json-experiment/json v0.0.0-20250813024750-ebf49471dced // indirect
	go4.org/mem v0.0.0-20240501181205-ae6ca9944745 // indirect
	go4.org/netipx v0.0.0-20231129151722-fdeea329fbba // indirect
	golang.org/x/crypto v0.46.0 // indirect
	golang.org/x/exp v0.0.0-20250620022241-b7579e27df2b // indirect
	golang.org/x/exp/typeparams v0.0.0-20240314144324-c7f7c6466f7f // indirect
	golang.org/x/mod v0.30.0 // indirect
	golang.org/x/tools v0.39.0 // indirect
	golang.org/x/tools/go/expect v0.1.1-deprecated // indirect
)

retract (
	v0.0.19
	v0.0.16
)

tool honnef.co/go/tools/cmd/staticcheck
