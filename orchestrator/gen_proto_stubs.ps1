param(
    [string[]]$ProtoFiles = @("proto/wisdev/wisdev.proto", "proto/llm/llm.proto")
)

$ErrorActionPreference = "Stop"

if (-not (Get-Command protoc -ErrorAction SilentlyContinue)) {
    throw "protoc is not installed or not in PATH."
}
if (-not (Get-Command protoc-gen-go -ErrorAction SilentlyContinue)) {
    throw "protoc-gen-go is not installed. Run: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest"
}
if (-not (Get-Command protoc-gen-go-grpc -ErrorAction SilentlyContinue)) {
    throw "protoc-gen-go-grpc is not installed. Run: go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest"
}

protoc `
  --proto_path "." `
  --go_out "." `
  --go_opt paths=source_relative `
  --go-grpc_out "." `
  --go-grpc_opt paths=source_relative `
  $ProtoFiles

Write-Host "Generated canonical Go stubs for:" ($ProtoFiles -join ", ")
