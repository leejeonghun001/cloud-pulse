#!/usr/bin/env python3
"""fake_release_server.py — a minimal HTTP server standing in for GitHub
Releases, used by scripts/smoke.sh and scripts/test-update.sh so their
`update`/`update --check` exercises never depend on real network access.

Usage:
    python3 fake_release_server.py <port> <assets-dir> <latest-tag>

Endpoints:
    GET /releases/latest       -> 302 Location: /releases/tag/<latest-tag>
    GET /releases/tag/<tag>    -> 200 text/plain "tag: <tag>" (informational only)
    GET /releases/download/<tag>/<asset> -> serves <assets-dir>/<tag>/<asset>
        (checksums.txt and binaries are looked up the same way; the
        caller is responsible for laying out <assets-dir>/<tag>/ to
        match what internal/selfupdate.Source expects for that tag)

Binds to 127.0.0.1 only. Intended for short-lived use in tests: the
caller starts it in the background, records its PID, and kills that PID
directly when done (never pkill -f).
"""
import http.server
import os
import sys
import urllib.parse


def main() -> int:
    if len(sys.argv) != 4:
        print(f"usage: {sys.argv[0]} <port> <assets-dir> <latest-tag>", file=sys.stderr)
        return 2

    port = int(sys.argv[1])
    assets_dir = os.path.abspath(sys.argv[2])
    latest_tag = sys.argv[3]

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):  # noqa: A002 - stdlib signature
            # Keep test output quiet; failures are asserted by the
            # caller via HTTP status/body, not by scraping server logs.
            pass

        def do_GET(self):  # noqa: N802 - stdlib method name
            parsed = urllib.parse.urlparse(self.path)
            path = parsed.path

            if path == "/releases/latest":
                self.send_response(302)
                self.send_header("Location", f"/releases/tag/{latest_tag}")
                self.end_headers()
                return

            if path.startswith("/releases/tag/"):
                tag = path[len("/releases/tag/"):]
                body = f"tag: {tag}\n".encode()
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                return

            if path.startswith("/releases/download/"):
                rest = path[len("/releases/download/"):]
                parts = rest.split("/", 1)
                if len(parts) != 2:
                    self.send_error(404, "not found")
                    return
                tag, asset = parts
                # Guard against path traversal via the URL; only a
                # plain tag/asset pair resolving inside assets_dir is
                # ever served.
                candidate = os.path.abspath(os.path.join(assets_dir, tag, asset))
                if not candidate.startswith(assets_dir + os.sep) and candidate != assets_dir:
                    self.send_error(403, "forbidden")
                    return
                if not os.path.isfile(candidate):
                    self.send_error(404, "not found")
                    return
                with open(candidate, "rb") as f:
                    data = f.read()
                self.send_response(200)
                self.send_header("Content-Type", "application/octet-stream")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
                return

            self.send_error(404, "not found")

    server = http.server.HTTPServer(("127.0.0.1", port), Handler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
