param(
    [string[]]$ProtoFiles = @(
        "orchestrator/proto/llm/llm.proto",
        "orchestrator/proto/wisdev/wisdev.proto"
    ),
    [string]$OutputDir = "sidecar/proto"
)

$ErrorActionPreference = "Stop"
$RepoRoot = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$PythonArgs = @()
$PreferredPythonPaths = @(
    (Join-Path $RepoRoot "sidecar\.venv\Scripts\python.exe"),
    (Join-Path $RepoRoot "sidecar\.venv\bin\python")
)
$PythonCommand = $null

foreach ($candidate in $PreferredPythonPaths) {
    if (Test-Path -LiteralPath $candidate) {
        $PythonCommand = $candidate
        break
    }
}

if (-not $PythonCommand) {
    $PythonExe = Get-Command python -ErrorAction SilentlyContinue
    if ($PythonExe) {
        $PythonCommand = $PythonExe.Source
    }
}
if (-not $PythonCommand) {
    $PythonExe = Get-Command py -ErrorAction SilentlyContinue
    if ($PythonExe) {
        $PythonCommand = $PythonExe.Source
        $PythonArgs = @("-3")
    }
}
if (-not $PythonCommand) {
    throw "python (or py launcher) is required to generate protobuf stubs. Create sidecar/.venv or install the sidecar requirements first."
}

& $PythonCommand @($PythonArgs + @("-m", "grpc_tools.protoc", "--version")) *> $null
if ($LASTEXITCODE -ne 0) {
    throw "grpcio-tools is required in the selected Python runtime. Install sidecar/requirements.txt first."
}

foreach ($proto in $ProtoFiles) {
    $protoPath = if ([System.IO.Path]::IsPathRooted($proto)) {
        $proto
    } else {
        Join-Path $RepoRoot $proto
    }

    if (-not (Test-Path -LiteralPath $protoPath)) {
        throw "Proto file not found: $protoPath"
    }

    $protoDir = Split-Path -Parent $protoPath
    $resolvedOutputDir = if ([System.IO.Path]::IsPathRooted($OutputDir)) {
        $OutputDir
    } else {
        Join-Path $RepoRoot $OutputDir
    }

    & $PythonCommand @(
        $PythonArgs +
        @(
            "-m", "grpc_tools.protoc",
            "--proto_path", $protoDir,
            "--python_out", $resolvedOutputDir,
            "--grpc_python_out", $resolvedOutputDir,
            $protoPath
        )
    )
    if ($LASTEXITCODE -ne 0) {
        throw "grpc_tools.protoc failed for $protoPath"
    }

    $baseName = [System.IO.Path]::GetFileNameWithoutExtension($protoPath)
    $grpcStubPath = Join-Path $resolvedOutputDir "${baseName}_pb2_grpc.py"
    if (Test-Path -LiteralPath $grpcStubPath) {
        $grpcStub = Get-Content -LiteralPath $grpcStubPath -Raw
        $grpcStub = $grpcStub -replace "(?m)^import ${baseName}_pb2 as ", "from . import ${baseName}_pb2 as "
        Set-Content -LiteralPath $grpcStubPath -Value $grpcStub -Encoding UTF8
    }
}

Write-Host "Generated Python protobuf message stubs for:" ($ProtoFiles -join ", ")
