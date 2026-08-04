const releaseNotesGenerator = require('@semantic-release/release-notes-generator');
// Use stdlib child_process rather than execa: this file is CommonJS and execa v9
// (what hoists at the repo root) is pure ESM, so `require('execa')` throws. It's
// also not a declared dependency.
const { execFile } = require('node:child_process');
const execFileAsync = require('node:util').promisify(execFile);

function printHelp() {
  console.log(`\nUsage: node generate_notes.js --from <tag-or-sha> [--to <tag-or-sha>] [--next-tag <tag>]\n\nOptions:\n  --from       Tag or commit to start from (mandatory)\n  --to         Tag or commit to end at (default: HEAD)\n  --next-tag   Tag label for the release being generated (default: value of --to).\n               Use this to show a real version (e.g. v1.15.1) in the notes header and\n               compare link while --to points at a branch/SHA.\n  --help       Show this help message\n`);
}

// Read the value that follows a flag, rejecting a missing value or another flag
// (e.g. `--from --to v1` must not silently set from === '--to').
function takeValue(args, i, flag) {
  const val = args[i + 1];
  if (val === undefined || val.startsWith('-')) {
    console.error(`Error: ${flag} requires a value.`);
    process.exit(1);
  }
  return val;
}

function parseArgs() {
  const args = process.argv.slice(2);
  let from = null;
  let to = 'HEAD';
  let nextTag = null;
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--help' || args[i] === '-h') {
      printHelp();
      process.exit(0);
    } else if (args[i] === '--from') {
      from = takeValue(args, i, '--from');
      i++;
    } else if (args[i] === '--to') {
      to = takeValue(args, i, '--to');
      i++;
    } else if (args[i] === '--next-tag') {
      nextTag = takeValue(args, i, '--next-tag');
      i++;
    }
  }
  if (!from) {
    printHelp();
    console.error('Error: --from parameter is required.');
    process.exit(1);
  }
  if (!nextTag) {
    nextTag = to;
  }
  return { from, to, nextTag };
}

// Strip a leading "v" so the notes header shows a clean semver (e.g. 1.15.1),
// matching semantic-release's nextRelease.version.
function stripV(tag) {
  return String(tag).replace(/^v/, '');
}

async function getCommits(from, to) {
  // Single `git log` over the range: -z delimits commits with NUL (bodies contain
  // newlines/blank lines, so a newline delimiter would be ambiguous). Each record
  // is "<hash>\n<raw body>"; the first line is the hash, the remainder the message.
  // maxBuffer: child_process defaults to 1 MB; a full GA-to-GA range can exceed
  // that, so raise it (execa's default was 100 MB).
  const { stdout } = await execFileAsync('git', [
    'log',
    '--reverse',
    '-z',
    '--format=%H%n%B',
    `${from}..${to}`,
  ], { maxBuffer: 64 * 1024 * 1024 });
  return stdout
    .split('\0')
    .filter(Boolean)
    .map((record) => {
      const nl = record.indexOf('\n');
      const hash = nl === -1 ? record : record.slice(0, nl);
      const message = nl === -1 ? '' : record.slice(nl + 1);
      return { hash: hash.trim(), message: message.trim() };
    });
}

(async () => {
  const { from, to, nextTag } = parseArgs();
  const repositoryUrl = 'https://github.com/weka/weka-operator';

  const commits = await getCommits(from, to);

  // Route the plugin's log output to stderr so stdout contains only the notes,
  // making it safe to redirect into a NOTES.md file. Cover the full signal
  // surface (log/error/warn/success/debug) so no plugin path throws a
  // TypeError mid-generation.
  const toStderr = (...a) => console.error(...a);
  const logger = {
    log: toStderr,
    error: toStderr,
    warn: toStderr,
    success: toStderr,
    debug: () => {},
  };

  const context = {
    commits,
    lastRelease: { gitTag: from, version: stripV(from) },
    nextRelease: { gitTag: nextTag, version: stripV(nextTag) },
    logger,
    cwd: process.cwd(),
    options: { repositoryUrl },
    env: process.env,
  };

  // Use the same conventionalcommits preset as .releaserc so the generated
  // notes match what semantic-release produces on release/v1.
  const pluginConfig = { preset: 'conventionalcommits', repositoryUrl };

  // Generate release notes
  const notes = await releaseNotesGenerator.generateNotes(pluginConfig, context);
  console.log(notes);
})();
