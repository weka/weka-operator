const releaseNotesGenerator = require('@semantic-release/release-notes-generator');
const execa = require('execa');

function printHelp() {
  console.log(`\nUsage: node generate_notes.js --from <tag-or-sha> [--to <tag-or-sha>]\n\nOptions:\n  --from   Tag or commit to start from (mandatory)\n  --to     Tag or commit to end at (default: HEAD)\n  --help   Show this help message\n`);
}

function parseArgs() {
  const args = process.argv.slice(2);
  let from = null;
  let to = 'HEAD';
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--help' || args[i] === '-h') {
      printHelp();
      process.exit(0);
    } else if (args[i] === '--from') {
      from = args[i + 1];
      i++;
    } else if (args[i] === '--to') {
      to = args[i + 1];
      i++;
    }
  }
  if (!from) {
    printHelp();
    console.error('Error: --from parameter is required.');
    process.exit(1);
  }
  return { from, to };
}

async function getCommits(from, to) {
  // Get commit hashes in range
  const { stdout } = await execa('git', ['rev-list', '--reverse', `${from}..${to}`]);
  const hashes = stdout.split('\n').filter(Boolean);

  // Get commit details
  const commits = [];
  for (const hash of hashes) {
    const { stdout: message } = await execa('git', ['log', '--format=%B', '-n', '1', hash]);
    commits.push({
      hash,
      message: message.trim(),
    });
  }
  return commits;
}

(async () => {
  const { from, to } = parseArgs();
  const repositoryUrl = 'https://github.com/weka/weka-operator';

  const commits = await getCommits(from, to);

  const context = {
    commits,
    lastRelease: { gitTag: from, version: from },
    nextRelease: { gitTag: to, version: to },
    logger: console,
    options: { repositoryUrl },
    env: process.env,
  };

  // Generate release notes
  const notes = await releaseNotesGenerator.generateNotes(
    { repositoryUrl }, // pluginConfig
    context
  );
  console.log(notes);
})();