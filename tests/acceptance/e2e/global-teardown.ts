import * as fs from 'node:fs';
import * as path from 'node:path';

const PID_FILE = path.join(__dirname, '.seed-pids.json');

export default async function globalTeardown() {
  if (!fs.existsSync(PID_FILE)) return;
  const { pids } = JSON.parse(fs.readFileSync(PID_FILE, 'utf8'));
  for (const pid of pids) {
    try { process.kill(pid, 'SIGTERM'); } catch {}
  }
  // Wait up to 2s, then SIGKILL anything still alive.
  await new Promise(r => setTimeout(r, 2000));
  for (const pid of pids) {
    try { process.kill(pid, 0); process.kill(pid, 'SIGKILL'); } catch {}
  }
  fs.unlinkSync(PID_FILE);
}
