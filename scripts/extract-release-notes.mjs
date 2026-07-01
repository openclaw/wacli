import fs from "node:fs";
import { pathToFileURL } from "node:url";

export function extractReleaseNotes(changelog, tag) {
  const version = String(tag).trim().replace(/^v/, "");
  if (!version) throw new Error("release tag is required");

  const lines = changelog.replace(/\r\n/g, "\n").split("\n");
  const headingPrefix = `## ${version} - `;
  const start = lines.findIndex((line) => line.startsWith(headingPrefix));
  if (start < 0) throw new Error(`missing changelog section for ${version}`);
  if (lines[start].slice(headingPrefix.length).trim() === "Unreleased") {
    throw new Error(`changelog section for ${version} is still Unreleased`);
  }

  let end = lines.findIndex((line, index) => index > start && line.startsWith("## "));
  if (end < 0) end = lines.length;
  const body = lines.slice(start + 1, end).join("\n").trim();
  if (!body) throw new Error(`changelog section for ${version} is empty`);
  return `## Changelog\n\n${body}\n`;
}

function parseArgs(argv) {
  const options = { changelog: "CHANGELOG.md" };
  for (let i = 0; i < argv.length; i += 1) {
    const name = argv[i];
    if (!["--tag", "--changelog", "--output"].includes(name) || !argv[i + 1]) {
      throw new Error(`invalid argument: ${name}`);
    }
    options[name.slice(2)] = argv[i + 1];
    i += 1;
  }
  if (!options.tag) throw new Error("--tag is required");
  if (!options.output) throw new Error("--output is required");
  return options;
}

function main() {
  const options = parseArgs(process.argv.slice(2));
  const changelog = fs.readFileSync(options.changelog, "utf8");
  fs.writeFileSync(options.output, extractReleaseNotes(changelog, options.tag));
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    main();
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
