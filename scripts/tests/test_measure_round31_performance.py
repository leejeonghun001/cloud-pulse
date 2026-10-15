import importlib.util
import http.server
import os
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "measure-round31-performance.py"
SPEC = importlib.util.spec_from_file_location("round31_performance", SCRIPT)
PERF = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(PERF)


def temporary_directory(prefix):
    return tempfile.TemporaryDirectory(prefix=prefix, dir=PERF.APPROVED_TMP)


class MeasurementMathTests(unittest.TestCase):
    def test_percentile_uses_linear_interpolation(self):
        values = list(range(1, 21))
        self.assertEqual(PERF.percentile(values, 0), 1)
        self.assertEqual(PERF.percentile(values, 50), 10.5)
        self.assertAlmostEqual(PERF.percentile(values, 95), 19.05)
        self.assertEqual(PERF.percentile(values, 100), 20)

    def test_empty_and_populated_summaries_are_explicit(self):
        self.assertEqual(PERF.summarize([]), {
            "n": 0, "mean": None, "median": None, "p95": None, "sample_max": None,
        })
        summary = PERF.summarize([1, 2, 3, 4])
        self.assertEqual(summary["n"], 4)
        self.assertEqual(summary["mean"], 2.5)
        self.assertEqual(summary["median"], 2.5)
        self.assertEqual(summary["sample_max"], 4)

    def test_process_cpu_uses_one_core_percentage_and_relative_time(self):
        sample = PERF.process_resource_sample(
            previous_ticks=100,
            previous_at=11.0,
            current=(110, 500, 700),
            now=12.0,
            clock_ticks=100,
            start=10.0,
        )
        self.assertEqual(sample["elapsed_s"], 2.0)
        self.assertEqual(sample["interval_s"], 1.0)
        self.assertEqual(sample["cpu_one_core_pct"], 10.0)
        self.assertEqual(sample["vmrss_kib"], 500)
        self.assertEqual(sample["vmhwm_kib"], 700)


class OwnedProcessTests(unittest.TestCase):
    def setUp(self):
        self.temp = temporary_directory("round31-process-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = {
            "PATH": os.environ.get("PATH", os.defpath),
            "HOME": str(self.root),
            "TMPDIR": str(self.root),
        }

    def child_command(self, mode="exit", exit_code=0):
        marker = self.root / f"marker-{mode}-{exit_code}"
        ready = self.root / f"ready-{mode}-{exit_code}"
        pidfile = self.root / f"pid-{mode}-{exit_code}"
        child = (
            "import signal,sys,time\n"
            "def stop(*_):\n"
            " open(sys.argv[1],'w').write('terminated')\n"
            " raise SystemExit(0)\n"
            "signal.signal(signal.SIGTERM,stop)\n"
            "open(sys.argv[2],'w').write('ready')\n"
            "while True: time.sleep(.02)\n"
        )
        parent = (
            "import os,subprocess,sys,time\n"
            "p=subprocess.Popen([sys.executable,'-c',sys.argv[1],sys.argv[2],sys.argv[3]])\n"
            "deadline=time.monotonic()+3\n"
            "while not os.path.exists(sys.argv[3]) and time.monotonic()<deadline: time.sleep(.01)\n"
            "open(sys.argv[4],'w').write(f'{p.pid} {os.getpgrp()}')\n"
            "if sys.argv[5].startswith('sleep'): time.sleep(20)\n"
            "raise SystemExit(int(sys.argv[6]))\n"
        )
        return [sys.executable, "-c", parent, child, str(marker), str(ready),
                str(pidfile), mode, str(exit_code)], marker, pidfile

    def assert_group_gone(self, pidfile):
        pid, pgid = map(int, pidfile.read_text().split())
        deadline = time.monotonic() + 2
        while PERF.process_group_exists(pgid) and time.monotonic() < deadline:
            time.sleep(0.02)
        self.assertFalse(PERF.process_group_exists(pgid), f"owned group {pgid} survived")
        proc_stat = Path(f"/proc/{pid}/stat")
        if proc_stat.exists():
            state = proc_stat.read_text().rsplit(")", 1)[1].split()[0]
            self.assertEqual(state, "Z", "owned descendant is still running")

    def test_success_and_nonzero_leader_exit_still_stop_descendants(self):
        for code in (0, 9):
            command, marker, pidfile = self.child_command(exit_code=code)
            if code:
                with self.assertRaisesRegex(RuntimeError, "command failed"):
                    PERF.run_bounded(command, cwd=SCRIPT.parents[2], env=self.env, timeout=5)
            else:
                result = PERF.run_bounded(command, cwd=SCRIPT.parents[2], env=self.env, timeout=5)
                self.assertEqual(result.returncode, 0)
            self.assertTrue(marker.exists(), "SIGTERM was not delivered to the remaining group member")
            self.assert_group_gone(pidfile)

    def test_timeout_stops_process_group_and_reaps_leader(self):
        command, marker, pidfile = self.child_command(mode="sleep")
        with self.assertRaisesRegex(RuntimeError, "exceeded"):
            PERF.run_bounded(command, cwd=SCRIPT.parents[2], env=self.env, timeout=0.6)
        self.assertTrue(marker.exists())
        self.assert_group_gone(pidfile)

    def test_sigterm_cancellation_runs_owned_process_cleanup(self):
        previous_term = signal.signal(signal.SIGTERM, signal.getsignal(signal.SIGTERM))
        previous_int = signal.signal(signal.SIGINT, signal.getsignal(signal.SIGINT))
        PERF.install_cancellation_handlers()
        try:
            for signum in (signal.SIGTERM, signal.SIGINT):
                PERF._CANCELLING = False
                command, marker, pidfile = self.child_command(mode=f"sleep-{signum}")
                stopped = threading.Event()

                def send_cancel_when_child_exists():
                    deadline = time.monotonic() + 2
                    while time.monotonic() < deadline and not pidfile.exists():
                        time.sleep(0.01)
                    if pidfile.exists():
                        os.kill(os.getpid(), signum)
                    stopped.set()

                sender = threading.Thread(target=send_cancel_when_child_exists, daemon=True)
                sender.start()
                with self.assertRaises(PERF.MeasurementCancelled):
                    PERF.run_bounded(command, cwd=SCRIPT.parents[2], env=self.env, timeout=5)
                sender.join(timeout=2)
                self.assertTrue(stopped.is_set())
                self.assertTrue(marker.exists())
                self.assert_group_gone(pidfile)
        finally:
            signal.signal(signal.SIGTERM, previous_term)
            signal.signal(signal.SIGINT, previous_int)
            PERF._CANCELLING = False

    def test_oversized_stdout_and_stderr_are_drained_then_killed(self):
        code = "import sys,time; sys.stdout.buffer.write(b'x'*100000); sys.stdout.flush(); sys.stderr.buffer.write(b'y'*100000); sys.stderr.flush(); time.sleep(10)"
        started = time.monotonic()
        with self.assertRaises(PERF.OutputLimitError):
            PERF.run_bounded([sys.executable, "-c", code], cwd=self.root, env=self.env,
                             timeout=5, stdout_limit=128, stderr_limit=128)
        self.assertLess(time.monotonic() - started, 2, "pipe readers must drain during execution")

    def test_profiled_command_drains_oversized_streams_while_sampling(self):
        code = "import sys,time; sys.stdout.buffer.write(b'x'*2000000); sys.stdout.flush(); sys.stderr.buffer.write(b'y'*2000000); sys.stderr.flush(); time.sleep(10)"
        original = PERF.spawn_captured

        def small_captures(*args, **kwargs):
            kwargs.setdefault("stdout_limit", 128)
            kwargs.setdefault("stderr_limit", 128)
            return original(*args, **kwargs)

        started = time.monotonic()
        with mock.patch.object(PERF, "spawn_captured", side_effect=small_captures):
            with self.assertRaises(PERF.OutputLimitError):
                PERF.run_profiled_command([sys.executable, "-c", code], self.env,
                                          self.root, 5, os.sysconf("SC_CLK_TCK"))
        self.assertLess(time.monotonic() - started, 2, "profile sampling must not block on full pipes")


class BoundedOutputAndServerTests(unittest.TestCase):
    def test_curl_capture_separates_headers_body_and_writeout(self):
        class LocalResponse(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                body = b"fixture-response-body"
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format, *_args):
                return

        server = http.server.HTTPServer(("127.0.0.1", 0), LocalResponse)
        thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True)
        thread.start()
        try:
            result = PERF.curl_request(f"http://127.0.0.1:{server.server_address[1]}/fixture", {
                "PATH": os.environ.get("PATH", os.defpath), "HOME": str(PERF.APPROVED_TMP),
                "TMPDIR": str(PERF.APPROVED_TMP),
            })
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=1)
        self.assertEqual(result["http_status"], 200)
        self.assertEqual(result["body_raw"], b"fixture-response-body")
        self.assertEqual(result["body_file_bytes"], len(result["body_raw"]))
        self.assertEqual(result["content_type"], "text/plain")
        self.assertIn(b" 200 ", result["headers_raw"])
        self.assertGreaterEqual(result["ttfb_ms"], 0)

    def test_capped_reader_retains_only_limit_while_draining_pipe(self):
        read_fd, write_fd = os.pipe()
        reader = PERF.CappedReader(os.fdopen(read_fd, "rb", buffering=0), 4)
        reader.start()
        os.write(write_fd, b"12345678")
        os.close(write_fd)
        reader.thread.join(timeout=1)
        self.assertFalse(reader.thread.is_alive())
        self.assertEqual(bytes(reader.data), b"1234")
        self.assertTrue(reader.overflow.is_set())

    def test_oversized_body_header_and_log_files_are_rejected_without_growth(self):
        with temporary_directory("round31-files-test-") as temp:
            root = Path(temp)
            for name, limit in (("body", 4), ("headers", 4), ("build.log", 6)):
                path = root / name
                if name == "build.log":
                    PERF.write_exclusive(path, b"1234", limit)
                    with self.assertRaises(PERF.OutputLimitError):
                        PERF.append_capped(path, b"567", limit)
                    self.assertEqual(path.read_bytes(), b"1234")
                else:
                    with self.assertRaises(PERF.OutputLimitError):
                        PERF.write_exclusive(path, b"12345", limit)
                    self.assertFalse(path.exists())

    def test_stalled_fake_hub_connection_is_closed_and_handler_joined(self):
        server, thread = PERF.start_fake_hub(0)
        client = socket.create_connection(server.server_address, timeout=1)
        client.sendall(b"POST /api/v1/agent/report HTTP/1.1\r\nHost: localhost\r\nContent-Length: 20\r\nAuthorization: Bearer round31-synthetic-agent-token-0123456789\r\n\r\n{")
        started = time.monotonic()
        try:
            PERF.stop_fake_hub(server, thread)
        finally:
            client.close()
        self.assertLess(time.monotonic() - started, PERF.FAKE_HUB_JOIN_TIMEOUT_S + 0.5)
        self.assertFalse(thread.is_alive())
        with server._lock:
            self.assertEqual(server._handlers, set())
            self.assertEqual(server._connections, set())

    def test_fake_hub_rejects_connections_above_owned_handler_limit(self):
        with mock.patch.object(PERF, "MAX_FAKE_HUB_HANDLERS", 1):
            server, thread = PERF.start_fake_hub(0)
            first = socket.create_connection(server.server_address, timeout=1)
            first.sendall(b"POST /api/v1/agent/report HTTP/1.1\r\nHost: localhost\r\n")
            deadline = time.monotonic() + 1
            while time.monotonic() < deadline:
                with server._lock:
                    if len(server._handlers) == 1:
                        break
                time.sleep(0.01)
            with server._lock:
                self.assertEqual(len(server._handlers), 1)
            second = socket.create_connection(server.server_address, timeout=1)
            second.settimeout(1)
            try:
                self.assertEqual(second.recv(1), b"")
                with server._lock:
                    self.assertLessEqual(len(server._handlers), 1)
            finally:
                second.close()
                first.close()
                PERF.stop_fake_hub(server, thread)


class EvidencePathTests(unittest.TestCase):
    def setUp(self):
        self.temp = temporary_directory("round31-path-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def test_symlink_and_non_directory_ancestors_are_refused(self):
        (self.root / ".verification").symlink_to(self.root, target_is_directory=True)
        with self.assertRaisesRegex(RuntimeError, "unsafe evidence ancestor"):
            PERF.guard_evidence_paths(self.root)

        other = self.root / "non-directory-root"
        other.mkdir()
        (other / ".verification").mkdir()
        (other / ".verification" / "round31").write_text("not a directory")
        with self.assertRaisesRegex(RuntimeError, "unsafe evidence ancestor"):
            PERF.guard_evidence_paths(other)

        third = self.root / "correction-link-root"
        (third / ".verification" / "round31").mkdir(parents=True)
        (third / ".verification" / "round31" / "performance-corrections").symlink_to(
            self.root, target_is_directory=True)
        with self.assertRaisesRegex(RuntimeError, "unsafe evidence ancestor"):
            PERF.guard_evidence_paths(third)

    def test_atomic_publish_succeeds_once_and_never_replaces_existing_destination(self):
        destination, correction = PERF.guard_evidence_paths(self.root)
        correction = PERF.ensure_stage_parent(self.root)
        stage = Path(tempfile.mkdtemp(prefix=PERF.STAGE_PREFIX, dir=correction))
        (stage / "evidence.txt").write_text("owned")
        PERF.publish_no_replace(stage, destination, correction)
        self.assertEqual((destination / "evidence.txt").read_text(), "owned")
        self.assertFalse(stage.exists())

        with self.assertRaises(FileExistsError):
            PERF.guard_evidence_paths(self.root)

    def test_publish_race_preserves_destination_and_cleans_only_owned_stage(self):
        destination, correction = PERF.guard_evidence_paths(self.root)
        correction = PERF.ensure_stage_parent(self.root)
        stage = Path(tempfile.mkdtemp(prefix=PERF.STAGE_PREFIX, dir=correction))
        (stage / "new.txt").write_text("new")
        marker = destination / "winner.txt"

        rename = PERF._rename_noreplace

        def competing_publisher(source_fd, source_name, destination_fd, destination_name):
            destination.mkdir()
            marker.write_text("do not delete")
            return rename(source_fd, source_name, destination_fd, destination_name)

        with mock.patch.object(PERF, "_rename_noreplace", side_effect=competing_publisher):
            with self.assertRaises(FileExistsError):
                PERF.publish_no_replace(stage, destination, correction)
        self.assertEqual(marker.read_text(), "do not delete")
        self.assertTrue((stage / "new.txt").exists())

        unrelated = correction / "unrelated-staging"
        unrelated.mkdir()
        keep = unrelated / "keep.txt"
        keep.write_text("keep")
        with self.assertRaisesRegex(RuntimeError, "refusing cleanup"):
            PERF.cleanup_owned_stage(unrelated, correction)
        self.assertTrue(keep.exists())
        PERF.cleanup_owned_stage(stage, correction)
        self.assertFalse(stage.exists())
        self.assertTrue(marker.exists())


if __name__ == "__main__":
    unittest.main()
