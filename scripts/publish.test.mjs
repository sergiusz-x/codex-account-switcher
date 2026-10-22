import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const root = fileURLToPath(new URL("../", import.meta.url));

test("publication stops on failures, recovers drafts, and never replaces a published release", t => {
  const dir = mkdtempSync(path.join(tmpdir(), "codex-switch-publish-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  for (const name of ["tools", "scripts", "internal/version"]) mkdirSync(path.join(dir, name), { recursive: true });
  copyFileSync(path.join(root, "scripts/publish-release.sh"), path.join(dir, "scripts/publish-release.sh"));
  copyFileSync(path.join(root, "scripts/finalize-release.sh"), path.join(dir, "scripts/finalize-release.sh"));
  writeFileSync(path.join(dir, "internal/version/VERSION"), "1.0.0\n");
  writeFileSync(path.join(dir, "CHANGELOG.md"), "# Synthetic release notes\n");
  writeFileSync(path.join(dir, ".gitignore"), "calls\n");
  // The package check has its own real GoReleaser smoke check. Here, inject
  // failures at publication boundaries without contacting any external service.
  writeFileSync(path.join(dir, "scripts/check-package.sh"), '#!/bin/sh\nprintf "check\\n" >> "$TEST_CALLS"\n[ -z "${TEST_CHECK_FAILURE:-}" ]\n');
  const tools = {
    gh: '#!/bin/sh\nprintf "gh %s\\n" "$1" >> "$TEST_CALLS"\nif [ "$1" = api ]; then\n  [ -z "${TEST_API_FAILURE:-}" ] || exit 1\n  printf "%s" "${TEST_RELEASE_STATE:-}"\nfi\n',
    goreleaser: '#!/bin/sh\nprintf "build\\n" >> "$TEST_CALLS"\n[ -z "${TEST_BUILD_FAILURE:-}" ]\n'
  };
  for (const [name, body] of Object.entries(tools)) {
    writeFileSync(path.join(dir, "tools", name), body);
    chmodSync(path.join(dir, "tools", name), 0o755);
  }
  const env = {
    ...process.env,
    PATH: `${path.join(dir, "tools")}:${process.env.PATH}`,
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_AUTHOR_NAME: "Synthetic publisher",
    GIT_AUTHOR_EMAIL: "publisher@example.invalid",
    GIT_COMMITTER_NAME: "Synthetic publisher",
    GIT_COMMITTER_EMAIL: "publisher@example.invalid",
    GITHUB_REPOSITORY: "example/synthetic",
    TEST_CALLS: path.join(dir, "calls")
  };
  const git = (...args) => execFileSync("git", args, { cwd: dir, env, stdio: "pipe" });
  git("init", "--initial-branch=main");
  git("add", ".");
  git("commit", "-m", "feat: synthetic release");
  git("tag", "v1.0.0");
  const run = overrides => {
    writeFileSync(env.TEST_CALLS, "");
    const result = spawnSync("sh", ["scripts/publish-release.sh", "1.0.0"], { cwd: dir, env: { ...env, ...overrides }, encoding: "utf8" });
    return { ...result, calls: readFileSync(env.TEST_CALLS, "utf8").trim().split("\n").filter(Boolean) };
  };
  for (const state of ["", "true"]) {
    const result = run({ TEST_RELEASE_STATE: state });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(result.calls, ["gh api", "build", "check"]);
  }
  for (const [overrides, expected] of [
    [{ TEST_API_FAILURE: "1" }, ["gh api"]],
    [{ TEST_RELEASE_STATE: "false" }, ["gh api"]],
    [{ TEST_BUILD_FAILURE: "1" }, ["gh api", "build"]],
    [{ TEST_CHECK_FAILURE: "1" }, ["gh api", "build", "check"]]
  ]) {
    const result = run(overrides);
    assert.notEqual(result.status, 0);
    assert.deepEqual(result.calls, expected);
  }
  const finalize = state => {
    writeFileSync(env.TEST_CALLS, "");
    const result = spawnSync("sh", ["scripts/finalize-release.sh", "1.0.0"], {
      cwd: dir,
      env: { ...env, TEST_RELEASE_STATE: state },
      encoding: "utf8"
    });
    return { ...result, calls: readFileSync(env.TEST_CALLS, "utf8").trim().split("\n").filter(Boolean) };
  };
  const verifiedDraft = finalize("true");
  assert.equal(verifiedDraft.status, 0, verifiedDraft.stderr);
  assert.deepEqual(verifiedDraft.calls, ["gh api", "gh release"]);
  const publicRelease = finalize("false");
  assert.notEqual(publicRelease.status, 0);
  assert.match(publicRelease.stderr, /verified draft/);
  assert.deepEqual(publicRelease.calls, ["gh api"]);

  git("tag", "v1.0.1");
  const outdated = run({});
  assert.notEqual(outdated.status, 0);
  assert.match(outdated.stderr, /newest release tag/);
  assert.deepEqual(outdated.calls, []);
});
