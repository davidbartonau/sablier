"""Mock model service for the Sablier opinionated stack.

Simulates a model server's lifecycle so the cold-start latency and
eviction behaviour become visible without needing a real GPU:

  1. On startup: sleep STARTUP_SECONDS to simulate weight loading.
  2. Once "loaded": start an HTTP server that responds 200 to /health.
  3. /          - identifying string with model name and configured delays.
  4. /infer     - sleeps INFERENCE_SECONDS, then returns the same string.
  5. /health    - 200 OK once loaded; 503 while still warming up.

Configurable via environment variables (with defaults):

  MODEL_NAME=mock-model
  STARTUP_SECONDS=3
  INFERENCE_SECONDS=0
  PORT=80
"""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
import signal
import sys
import threading
import time

MODEL_NAME = os.environ.get("MODEL_NAME", "mock-model")
STARTUP_SECONDS = float(os.environ.get("STARTUP_SECONDS", "3"))
INFERENCE_SECONDS = float(os.environ.get("INFERENCE_SECONDS", "0"))
PORT = int(os.environ.get("PORT", "80"))

# Set once startup completes; /health checks this flag.
_ready = threading.Event()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        # Keep stdout tidy; flip to sys.stderr.write(...) if you want access logs.
        pass

    def _write(self, status: int, body: bytes, content_type: str = "text/plain"):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            if _ready.is_set():
                self._write(200, b"ok\n")
            else:
                self._write(503, b"warming\n")
            return

        if self.path.startswith("/infer"):
            time.sleep(INFERENCE_SECONDS)

        body = (
            f"Hello from {MODEL_NAME} "
            f"(startup={STARTUP_SECONDS}s, infer={INFERENCE_SECONDS}s)\n"
        ).encode()
        self._write(200, body)


def main():
    print(
        f"[{MODEL_NAME}] simulating startup ({STARTUP_SECONDS}s)...",
        flush=True,
    )
    time.sleep(STARTUP_SECONDS)
    _ready.set()
    print(f"[{MODEL_NAME}] ready, listening on :{PORT}", flush=True)

    httpd = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)

    # Stop cleanly on SIGTERM (sent by `docker stop`) and SIGINT (Ctrl-C).
    # Without this, http.server ignores SIGTERM and Docker waits the full
    # stop-timeout (default 10s) before SIGKILLing — making evictions look
    # artificially slow. Real model servers should handle SIGTERM the same way.
    def _shutdown(signum, _frame):
        print(f"[{MODEL_NAME}] received signal {signum}, shutting down", flush=True)
        threading.Thread(target=httpd.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, _shutdown)
    signal.signal(signal.SIGINT, _shutdown)

    try:
        httpd.serve_forever()
    finally:
        httpd.server_close()


if __name__ == "__main__":
    sys.exit(main())
