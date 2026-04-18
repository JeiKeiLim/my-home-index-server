// Nested module — keeps the acceptance-test stubs (which reference
// packages implemented in later jobs) out of the parent module's
// `./...` expansion until those packages exist. When the feature code
// lands, running `go test ./...` from this directory compiles the stubs
// against the parent module via the replace directive below.
module github.com/JeiKeiLim/my-home-index-server/tests/acceptance

go 1.24.0

require (
	github.com/JeiKeiLim/my-home-index-server v0.0.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/ebitengine/purego v0.10.0 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/joho/godotenv v1.5.1 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/shirou/gopsutil/v4 v4.26.3 // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/sys v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/JeiKeiLim/my-home-index-server => ../..
