import { existsSync } from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

export function wisdevAgentOsRootFromModule() {
  return path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
}

export function resolveWisdevInvocation({
  repoRoot = wisdevAgentOsRootFromModule(),
  args = [],
  platform = process.platform,
} = {}) {
  const distName = platform === 'win32' ? 'wisdev.exe' : 'wisdev';
  const distPath = path.join(repoRoot, 'dist', distName);
  if (existsSync(distPath)) {
    return { command: distPath, args, cwd: repoRoot };
  }

  if (platform === 'win32') {
    return {
      command: 'powershell',
      args: [
        '-NoProfile',
        '-ExecutionPolicy',
        'Bypass',
        '-File',
        path.join(repoRoot, 'scripts', 'wisdev.ps1'),
        ...args,
      ],
      cwd: repoRoot,
    };
  }

  return {
    command: path.join(repoRoot, 'scripts', 'wisdev.sh'),
    args,
    cwd: repoRoot,
  };
}

export function runWisdev({
  repoRoot = wisdevAgentOsRootFromModule(),
  args = [],
  spawnImpl = spawnSync,
  env = process.env,
} = {}) {
  const invocation = resolveWisdevInvocation({ repoRoot, args });
  const result = spawnImpl(invocation.command, invocation.args, {
    cwd: invocation.cwd,
    encoding: 'utf8',
    env,
    stdio: 'pipe',
    shell: false,
  });

  return {
    status: result.status === 0 ? 'pass' : 'fail',
    exitCode: result.status,
    stdout: String(result.stdout || ''),
    stderr: String(result.stderr || ''),
    command: [invocation.command, ...invocation.args].join(' '),
  };
}

async function main() {
  const args = process.argv.slice(2);
  const report = runWisdev({ args });
  if (report.stdout) process.stdout.write(report.stdout);
  if (report.stderr) process.stderr.write(report.stderr);
  if (report.exitCode) process.exitCode = report.exitCode;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  });
}
