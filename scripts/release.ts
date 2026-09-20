#!/usr/bin/env bun

const bumps = ["major", "minor", "patch"] as const;
type Bump = (typeof bumps)[number];

function usage(): never {
  console.error("usage: scripts/release.ts major|minor|patch");
  process.exit(1);
}

let cwd = process.cwd();

function git(args: string[], opts?: { allowFail?: boolean }): string {
  const result = Bun.spawnSync(["git", ...args], {
    cwd,
    stdout: "pipe",
    stderr: "pipe",
  });
  if (result.exitCode !== 0 && !opts?.allowFail) {
    const err = result.stderr.toString().trim();
    console.error(err || `git ${args.join(" ")} failed`);
    process.exit(result.exitCode ?? 1);
  }
  return result.stdout.toString().trim();
}

const bump = process.argv[2];
if (!bump || !bumps.includes(bump as Bump)) {
  usage();
}

cwd = git(["rev-parse", "--show-toplevel"]);

if (git(["status", "--porcelain"])) {
  console.error("working tree is not clean; commit or stash first");
  process.exit(1);
}

// Latest vX.Y.Z tag is the current version. No extra VERSION file: the
// release workflow already publishes from the tag that is pushed.
const tags = git(["tag", "-l", "v*.*.*", "--sort=-v:refname"]);
let current = tags.split("\n").find(Boolean) ?? "v0.0.0";

const match = /^v(\d+)\.(\d+)\.(\d+)$/.exec(current);
if (!match) {
  console.error(`latest tag ${current} is not vMAJOR.MINOR.PATCH`);
  process.exit(1);
}

let major = Number(match[1]);
let minor = Number(match[2]);
let patch = Number(match[3]);

switch (bump as Bump) {
  case "major":
    major += 1;
    minor = 0;
    patch = 0;
    break;
  case "minor":
    minor += 1;
    patch = 0;
    break;
  case "patch":
    patch += 1;
    break;
}

const next = `v${major}.${minor}.${patch}`;

if (git(["rev-parse", next], { allowFail: true })) {
  console.error(`tag ${next} already exists`);
  process.exit(1);
}

console.log(`current: ${current}`);
console.log(`next:    ${next}`);

git(["tag", "-a", next, "-m", next]);
git(["push", "origin", `refs/tags/${next}`]);

console.log(`published ${next}`);
