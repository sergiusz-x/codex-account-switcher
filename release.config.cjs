module.exports = {
  branches: ["main"],
  tagFormat: "v${version}",
  plugins: [
    ["@semantic-release/commit-analyzer", { preset: "conventionalcommits" }],
    ["@semantic-release/release-notes-generator", { preset: "conventionalcommits" }],
    ["@semantic-release/changelog", { changelogFile: "CHANGELOG.md" }],
    ["@semantic-release/exec", {
      prepareCmd: "node scripts/prepare-release.mjs ${nextRelease.version}",
      publishCmd: "sh scripts/publish-release.sh ${nextRelease.version}"
    }],
    ["@semantic-release/git", {
      assets: ["internal/version/VERSION", "CHANGELOG.md"],
      message: "chore(release): ${nextRelease.version} [skip ci]"
    }]
  ]
};
