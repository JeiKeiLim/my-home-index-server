import { spawn, ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';

// Seed listeners spawned here (kept running across the whole suite).
// Torn down by global-teardown.
const SEED_LISTENERS: { port: number; cmd: string; args: string[] }[] = [
  { port: 40190, cmd: 'python3', args: ['-c', `
import http.server, socketserver, signal, sys
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
socketserver.TCPServer(('0.0.0.0', 40190), http.server.SimpleHTTPRequestHandler).serve_forever()
`]},
];

const PID_FILE = path.join(__dirname, '.seed-pids.json');

export default async function globalSetup() {
  const pids: number[] = [];
  for (const l of SEED_LISTENERS) {
    const child = spawn(l.cmd, l.args, { detached: true, stdio: 'ignore' });
    if (!child.pid) throw new Error(`failed to spawn seed listener on ${l.port}`);
    child.unref();
    pids.push(child.pid);
  }
  fs.writeFileSync(PID_FILE, JSON.stringify({ pids }));
  // Give seed listeners a moment to bind before tests ask the dashboard to list them.
  await new Promise(r => setTimeout(r, 300));
}
