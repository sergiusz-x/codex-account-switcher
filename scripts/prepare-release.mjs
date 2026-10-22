import { writeFile } from "node:fs/promises";

const version = process.argv[2];
if (!/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(version ?? "") || version.trim() !== version) {
  throw new Error("Expected a plain MAJOR.MINOR.PATCH release version.");
}
await writeFile("internal/version/VERSION", `${version}\n`);
