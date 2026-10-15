"""Static safety checks for the bounded local Docker Compose specimen."""

from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[2]


def read(name: str) -> str:
    return (ROOT / name).read_text(encoding="utf-8")


class ComposeSpecimenTests(unittest.TestCase):
    def test_hub_only_cgo_free_multistage_build_and_nonroot_runtime(self) -> None:
        dockerfile = read("Dockerfile")
        self.assertRegex(dockerfile, r"(?m)^FROM golang:1\.25(?:\.|-)\S* AS build$")
        self.assertIn("ENV CGO_ENABLED=0 GOOS=linux", dockerfile)
        self.assertIn("./cmd/hub", dockerfile)
        self.assertNotIn("./cmd/agent", dockerfile)
        self.assertIn("FROM gcr.io/distroless/static-debian12:nonroot", dockerfile)
        self.assertIn("USER 65532:65532", dockerfile)
        self.assertIn("/etc/ssl/certs/ca-certificates.crt", dockerfile)
        self.assertIn("/out/data /data", dockerfile)
        self.assertIn("CP_DATA_DIR=/data", dockerfile)

    def test_compose_requires_token_and_binds_only_localhost(self) -> None:
        compose = read("compose.yaml")
        env = read(".env.example")
        self.assertIn("CP_AGENT_TOKEN: ${CP_AGENT_TOKEN:?Set CP_AGENT_TOKEN in .env}", compose)
        compose_keys = {
            line.strip().split(":", 1)[0]
            for line in compose.splitlines()
            if line.strip().startswith("CP_")
        }
        example_keys = {
            line.split("=", 1)[0]
            for line in env.splitlines()
            if line and not line.startswith("#")
        }
        self.assertEqual(compose_keys, example_keys)
        self.assertIn("host_ip: 127.0.0.1", compose)
        self.assertIn('published: "18091"', compose)
        self.assertNotIn('published: "8090"', compose)
        self.assertNotIn('CP_ALLOWED_CIDRS: "*"', compose)
        self.assertIn("read_only: true", compose)
        self.assertIn("cap_drop:\n      - ALL", compose)
        self.assertIn("no-new-privileges:true", compose)
        self.assertRegex(env, r"(?m)^CP_AGENT_TOKEN=$")
        self.assertIn("CP_UPDATE_CHECK=false", env)
        self.assertIn("CP_BILLING=off", env)

    def test_compose_cidr_matches_allowlist_without_wildcard(self) -> None:
        compose = read("compose.yaml")
        env = read(".env.example")
        subnet = re.search(r"(?m)^\s+- subnet: (\S+)$", compose)
        self.assertIsNotNone(subnet)
        self.assertIn(subnet.group(1), env)
        self.assertIn("CP_ALLOWED_CIDRS=127.0.0.0/8,::1/128,", env)
        self.assertNotRegex(env, r"(?m)^CP_ALLOWED_CIDRS=.*\*")

    def test_no_provider_secrets_or_custom_endpoint_opt_ins(self) -> None:
        specimen = read("compose.yaml") + read(".env.example")
        for forbidden in (
            "AWS_ACCESS_KEY_ID",
            "AWS_SECRET_ACCESS_KEY",
            "CP_R2_API_TOKEN",
            "CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS",
            "CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS",
            "CP_STORAGE_FAKE_BASE_URL",
            "dev-token",
        ):
            self.assertNotIn(forbidden, specimen)

    def test_build_context_excludes_local_state_but_keeps_embedded_inputs(self) -> None:
        ignore = read(".dockerignore")
        for pattern in (
            ".git/**",
            ".verification/**",
            ".slim/**",
            ".env",
            ".env.*",
            "!.env.example",
            "data/",
            "bin/",
            "dist/",
            "*.db",
            "*.exe",
            "node_modules/",
        ):
            self.assertIn(pattern, ignore)
        self.assertNotRegex(ignore, r"(?m)^(?:\*|web/|web/assets/)$")
        for embedded in ("go.mod", "go.sum", "web/embed.go", "web/index.html", "web/assets", "internal/i18n/catalog"):
            self.assertNotIn(embedded + "\n", ignore)

    def test_gitignore_ignores_only_the_local_env_file(self) -> None:
        lines = read(".gitignore").splitlines()
        self.assertIn(".env", lines)
        self.assertNotIn(".env.example", lines)

    def test_native_smoke_isolates_env_bounds_requests_and_reaps_own_pid(self) -> None:
        readme = read("README.md")
        native = readme.split("### Native source smoke (offline)\n", 1)[1].split("\n## Documentation", 1)[0]
        runtime_env = native.split("env -i ", 1)[1].split('"$tmp/cloud-pulse-hub"', 1)[0]
        env_names = set(re.findall(r"\b([A-Z][A-Z0-9_]*)=", runtime_env))
        self.assertEqual(env_names, {
            "PATH", "TMPDIR", "HOME", "CP_AGENT_TOKEN", "CP_LISTEN", "CP_DATA_DIR",
            "CP_ALLOWED_CIDRS", "CP_UPDATE_CHECK", "CP_BILLING",
        })
        self.assertIn("env -i PATH=/usr/bin:/bin", native)
        self.assertIn("--connect-timeout 1 --max-time 2", native)
        self.assertIn('SMOKE_PORT="${SMOKE_PORT:-18091}"', native)
        self.assertIn("port != 8090", native)
        self.assertIn('s.bind(("127.0.0.1", port))', native)
        self.assertIn('SMOKE_ADDR="127.0.0.1:$SMOKE_PORT"', native)
        self.assertIn('SMOKE_BASE_URL="http://$SMOKE_ADDR"', native)
        self.assertIn('CP_LISTEN="$SMOKE_ADDR"', native)
        self.assertIn('"$SMOKE_BASE_URL/healthz"', native)
        self.assertIn('base = os.environ["SMOKE_BASE_URL"]', native)
        self.assertNotIn("127.0.0.1:18091", native)

        compose = readme.split("### Docker Compose\n", 1)[1].split("### Native source smoke", 1)[0]
        self.assertIn("--connect-timeout 2 --max-time 5", compose)

        cleanup = native.split("cleanup() {", 1)[1].split("\n}", 1)[0]
        self.assertIn('while [ "$ticks" -lt 3 ]', cleanup)
        self.assertLess(cleanup.index('kill -TERM "$pid"'), cleanup.index('kill -KILL "$pid"'))
        self.assertLess(cleanup.index('kill -KILL "$pid"'), cleanup.index('wait "$pid"'))
        self.assertNotIn("pkill", cleanup)
        for line in cleanup.splitlines():
            if line.strip().startswith("kill "):
                self.assertIn('"$pid"', line)


if __name__ == "__main__":
    unittest.main()
