import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const root = fileURLToPath(new URL("../", import.meta.url));
const archiveName = "codex_switch_linux_amd64.tar.gz";

function fixture(t, version = "1.0.0") {
  const dir = mkdtempSync(path.join(tmpdir(), "codex-switch-install-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  for (const name of ["tools", "assets", "payload", "destination with spaces", "codex-home"]) {
    mkdirSync(path.join(dir, name));
  }
  const binary = path.join(dir, "payload/codex_switch");
  writeFileSync(binary, `#!/bin/sh\ncase "$1" in\n--version) echo "${version} (test; linux/amd64)" ;;\n--help) echo "Usage: codex_switch" ;;\n*) exit 1 ;;\nesac\n`);
  chmodSync(binary, 0o755);
  execFileSync("tar", ["-czf", path.join(dir, "assets", archiveName), "-C", path.join(dir, "payload"), "codex_switch"]);
  const checksum = createHash("sha256").update(readFileSync(path.join(dir, "assets", archiveName))).digest("hex");
  writeFileSync(path.join(dir, "assets/checksums.txt"), `${checksum}  ${archiveName}\n`);
  writeFileSync(path.join(dir, "codex-home/auth.json"), '{"synthetic":"must remain unchanged"}\n');
  const tools = {
    uname: '#!/bin/sh\ncase "$1" in\n-s) echo "${TEST_OS:-Linux}" ;;\n-m) echo "${TEST_ARCH:-x86_64}" ;;\nesac\n',
    curl: `#!${process.execPath}
const fs = require('node:fs');
const path = require('node:path');
const args = process.argv.slice(2);
fs.appendFileSync(process.env.TEST_CALLS, JSON.stringify(args) + '\\n');
if (process.env.TEST_DOWNLOAD_FAILURE) process.exit(22);
const output = args[args.indexOf('--output') + 1];
const url = args.find(arg => arg.startsWith('https://'));
fs.copyFileSync(path.join(process.env.TEST_ASSETS, path.basename(new URL(url).pathname)), output);
`,
    gh: `#!${process.execPath}
const fs = require('node:fs');
const path = require('node:path');
const args = process.argv.slice(2);
fs.appendFileSync(process.env.TEST_CALLS, JSON.stringify(args) + '\\n');
if (process.env.TEST_DOWNLOAD_FAILURE) process.exit(1);
const dir = args[args.indexOf('--dir') + 1];
for (const name of ['${archiveName}', 'checksums.txt']) fs.copyFileSync(path.join(process.env.TEST_ASSETS, name), path.join(dir, name));
`
  };
  for (const [name, body] of Object.entries(tools)) {
    writeFileSync(path.join(dir, "tools", name), body);
    chmodSync(path.join(dir, "tools", name), 0o755);
  }
  const destination = path.join(dir, "destination with spaces/codex_switch");
  writeFileSync(destination, "original executable\n");
  const env = {
    ...process.env,
    PATH: `${path.join(dir, "tools")}:${process.env.PATH}`,
    CODEX_HOME: path.join(dir, "codex-home"),
    CODEX_SWITCH_REPOSITORY: "example/switcher",
    CODEX_SWITCH_VERSION: version,
    CODEX_SWITCH_BIN_DIR: path.dirname(destination),
    TEST_ASSETS: path.join(dir, "assets"),
    TEST_CALLS: path.join(dir, "calls")
  };
  const run = (args = [], overrides = {}, script = path.join(root, "install.sh")) =>
    spawnSync("sh", [script, ...args], { cwd: dir, env: { ...env, ...overrides }, encoding: "utf8" });
  return { dir, destination, env, run };
}

function unchanged(f) {
  assert.equal(readFileSync(f.destination, "utf8"), "original executable\n");
  assert.deepEqual(readdirSync(path.dirname(f.destination)), ["codex_switch"]);
  assert.equal(readFileSync(path.join(f.dir, "codex-home/auth.json"), "utf8"), '{"synthetic":"must remain unchanged"}\n');
}

test("install and update an executable in a path with spaces, preserving credentials", t => {
  const f = fixture(t);
  for (let i = 0; i < 2; i++) {
    const result = f.run();
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /Installed codex_switch 1\.0\.0/);
    assert.equal(execFileSync(f.destination, ["--version"], { encoding: "utf8" }).trim(), "1.0.0 (test; linux/amd64)");
  }
  assert.equal(readFileSync(path.join(f.dir, "codex-home/auth.json"), "utf8"), '{"synthetic":"must remain unchanged"}\n');
  assert.deepEqual(readdirSync(path.dirname(f.destination)), ["codex_switch"]);
});

test("the actual GoReleaser executable installs and updates", { skip: !process.env.CODEX_SWITCH_TEST_PACKAGE_DIR }, t => {
  const f = fixture(t);
  const expected = readFileSync(path.join(root, "internal/version/VERSION"), "utf8").trim();
  for (const name of [archiveName, "checksums.txt"]) {
    copyFileSync(path.join(process.env.CODEX_SWITCH_TEST_PACKAGE_DIR, name), path.join(f.dir, "assets", name));
  }
  for (let i = 0; i < 2; i++) {
    const result = f.run(["--version", expected]);
    assert.equal(result.status, 0, result.stderr);
    assert.ok(execFileSync(f.destination, ["--version"], { encoding: "utf8" }).startsWith(`${expected} (`));
  }
  assert.equal(readFileSync(path.join(f.dir, "codex-home/auth.json"), "utf8"), '{"synthetic":"must remain unchanged"}\n');
});

test("private installations download with gh without passing tokens to curl", t => {
  const f = fixture(t);
  const result = f.run(["--github-cli"]);
  assert.equal(result.status, 0, result.stderr);
  const calls = readFileSync(f.env.TEST_CALLS, "utf8").trim().split("\n").map(JSON.parse);
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].slice(0, 5), ["release", "download", "v1.0.0", "--repo", "example/switcher"]);
});

test("generated release installer knows its repository and pinned version", t => {
  const version = readFileSync(path.join(root, "internal/version/VERSION"), "utf8").trim();
  const f = fixture(t, version);
  execFileSync("sh", [path.join(root, "scripts/prepare-installer.sh")], {
    cwd: f.dir,
    env: {
      ...f.env,
      GITHUB_REPOSITORY: "example/generated",
      CODEX_SWITCH_RELEASE_ASSET_DIR: path.join(f.dir, "release-assets")
    }
  });
  const result = f.run([], { CODEX_SWITCH_REPOSITORY: "", CODEX_SWITCH_VERSION: "" }, path.join(f.dir, "release-assets/install.sh"));
  assert.equal(result.status, 0, result.stderr);
  const calls = readFileSync(f.env.TEST_CALLS, "utf8");
  assert.ok(calls.includes(`example/generated/releases/download/v${version}/`));
});

test("a failed download leaves the installed executable unchanged", t => {
  const f = fixture(t);
  const result = f.run([], { TEST_DOWNLOAD_FAILURE: "1" });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /Download failed/);
  unchanged(f);
});

test("a checksum mismatch leaves the installed executable unchanged", t => {
  const f = fixture(t);
  writeFileSync(path.join(f.dir, "assets/checksums.txt"), `${"0".repeat(64)}  ${archiveName}\n`);
  const result = f.run();
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /checksum mismatch/);
  unchanged(f);
});

test("a mismatched embedded version is rejected before installation", t => {
  const f = fixture(t);
  const result = f.run(["--version", "1.0.1"]);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /does not match/);
  unchanged(f);
});

test("failed replacement cleans the pending file and preserves the existing program", t => {
  const f = fixture(t);
  writeFileSync(path.join(f.dir, "tools/mv"), "#!/bin/sh\nexit 1\n");
  chmodSync(path.join(f.dir, "tools/mv"), 0o755);
  assert.notEqual(f.run().status, 0);
  unchanged(f);
});

test("unsupported operating systems and architectures fail before downloading", t => {
  const f = fixture(t);
  for (const env of [{ TEST_OS: "Darwin" }, { TEST_OS: "MINGW64_NT" }, { TEST_ARCH: "aarch64" }]) {
    assert.notEqual(f.run([], env).status, 0);
    assert.equal(existsSync(f.env.TEST_CALLS), false);
    unchanged(f);
  }
});

test("invalid repositories, versions, and missing arguments fail before downloading", t => {
  const f = fixture(t);
  for (const args of [["--repo", "../bad/repo"], ["--repo", "bad;command/repo"], ["--repo", "example/repo\nbad"], ["--version", "01.0.0"], ["--version", "1.0.0-beta"], ["--version", "1.0.0\n2.0.0"], ["--bin-dir", ""], ["--version"], ["--unknown"]]) {
    assert.notEqual(f.run(args).status, 0, JSON.stringify(args));
    assert.equal(existsSync(f.env.TEST_CALLS), false);
    unchanged(f);
  }
});

test("symlink destinations are rejected without changing their target", t => {
  const f = fixture(t);
  const target = path.join(f.dir, "symlink-target");
  writeFileSync(target, "keep me\n");
  rmSync(f.destination);
  symlinkSync(target, f.destination);
  const result = f.run();
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /symlink/);
  assert.equal(readFileSync(target, "utf8"), "keep me\n");
});
