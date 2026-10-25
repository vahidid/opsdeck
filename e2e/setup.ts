import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import path from "node:path";

// Starts the fixture server (go run ./tools/e2e, or OPSDECK_E2E_BIN when it
// is built already) and passes its login URL and password to the tests.
export default async function setup() {
  const root = path.resolve(import.meta.dirname, "..");
  const port = process.env.OPSDECK_E2E_PORT ?? "7799";
  const bin = process.env.OPSDECK_E2E_BIN;
  const child = bin
    ? spawn(bin, ["-port", port], { cwd: root, stdio: ["ignore", "pipe", "inherit"] })
    : spawn("go", ["run", "./tools/e2e", "-port", port], { cwd: root, stdio: ["ignore", "pipe", "inherit"], detached: true });

  const line = await new Promise<string>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("the e2e server did not start in 3 minutes")), 180_000);
    child.on("exit", (code) => reject(new Error(`the e2e server exited (${code})`)));
    createInterface({ input: child.stdout! }).once("line", (l) => {
      clearTimeout(timer);
      resolve(l);
    });
  });
  const info = JSON.parse(line) as { url: string; base: string; password: string; hostDir: string };
  process.env.OPSDECK_E2E = JSON.stringify(info);

  return async () => {
    // go run starts the server as its child: stop the whole group
    if (!bin && child.pid) process.kill(-child.pid, "SIGINT");
    else child.kill("SIGINT");
  };
}
