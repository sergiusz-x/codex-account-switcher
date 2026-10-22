import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";
import { Writable } from "node:stream";
import { fileURLToPath } from "node:url";
import semanticRelease from "semantic-release";
import config from "../release.config.cjs";
import test from "node:test";

const root = fileURLToPath(new URL("../", import.meta.url));
const require = createRequire(import.meta.url);

test("semantic-release versions and tags a local repository from conventional commits", async t => {
  const dir = mkdtempSync(path.join(tmpdir(), "codex-switch-release-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const cwd = path.join(dir, "work");
  const remote = path.join(dir, "remote.git");
  mkdirSync(cwd);
  const env = {
    PATH: process.env.PATH,
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_AUTHOR_NAME: "Synthetic release test",
    GIT_AUTHOR_EMAIL: "release@example.invalid",
    GIT_COMMITTER_NAME: "Synthetic release test",
    GIT_COMMITTER_EMAIL: "release@example.invalid"
  };
  const git = (...args) => execFileSync("git", args, { cwd, env, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
  git("init", "--bare", "--initial-branch=main", remote);
  git("init", "--initial-branch=main");
  git("remote", "add", "origin", remote);
  mkdirSync(path.join(cwd, "internal/version"), { recursive: true });
  mkdirSync(path.join(cwd, "scripts"));
  copyFileSync(path.join(root, "scripts/prepare-release.mjs"), path.join(cwd, "scripts/prepare-release.mjs"));
  writeFileSync(path.join(cwd, "internal/version/VERSION"), "1.0.0\n");
  writeFileSync(path.join(cwd, "CHANGELOG.md"), "# Changelog\n");
  writeFileSync(path.join(cwd, ".gitignore"), "published-version\n");
  const plugins = config.plugins.map(([name, options]) => [require.resolve(name), {
    ...options,
    ...(name === "@semantic-release/exec" ? {
      publishCmd: "node -e \"require('node:fs').writeFileSync('published-version', '${nextRelease.version}')\""
    } : {})
  }]);
  const quiet = new Writable({ write(_chunk, _encoding, done) { done(); } });
  const release = () => semanticRelease({ ...config, plugins, repositoryUrl: `file://${remote}`, ci: false }, { cwd, env, stdout: quiet, stderr: quiet });
  const commit = message => {
    writeFileSync(path.join(cwd, "change"), message);
    git("add", ".");
    git("commit", "-m", message);
    git("push", "origin", "main");
  };
  for (const [message, expected] of [
    ["feat: initial account switcher", "1.0.0"],
    ["fix: preserve outgoing credentials", "1.0.1"],
    ["feat: add account names", "1.1.0"],
    ["feat!: change the profile format", "2.0.0"]
  ]) {
    commit(message);
    const result = await release();
    assert.equal(result.nextRelease.version, expected);
    assert.equal(readFileSync(path.join(cwd, "internal/version/VERSION"), "utf8"), `${expected}\n`);
    assert.equal(git("describe", "--tags", "--exact-match", "HEAD"), `v${expected}`);
    assert.equal(git("show", `v${expected}:internal/version/VERSION`), expected);
    assert.match(readFileSync(path.join(cwd, "CHANGELOG.md"), "utf8"), new RegExp(expected.replaceAll(".", "\\.")));
    assert.equal(readFileSync(path.join(cwd, "published-version"), "utf8"), expected);
    assert.equal(git("status", "--porcelain"), "");
  }
  commit("docs: explain installation");
  assert.equal(await release(), false);
  assert.equal(git("tag", "--list").split("\n").length, 4);
});

test("invalid release versions never overwrite the embedded version file", t => {
  const cwd = mkdtempSync(path.join(tmpdir(), "codex-switch-version-test-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  mkdirSync(path.join(cwd, "internal/version"), { recursive: true });
  const file = path.join(cwd, "internal/version/VERSION");
  writeFileSync(file, "1.0.0\n");
  for (const version of ["", "v1.0.0", "01.0.0", "1.0.0-beta.1", "1.0.0\n2.0.0", "1.0.0\n"]) {
    const result = spawnSync(process.execPath, [path.join(root, "scripts/prepare-release.mjs"), version], { cwd });
    assert.notEqual(result.status, 0, version);
    assert.equal(readFileSync(file, "utf8"), "1.0.0\n");
  }
});
