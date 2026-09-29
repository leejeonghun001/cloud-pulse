// ports.test.mjs — unit tests for assets/js/core/ports.js (listening
// port / Docker container formatting helpers used by the host detail
// "Services & ports" card and the overview "Containers" column).
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  formatPublishedPort,
  formatListeningAddress,
  sortPorts,
  sortContainers,
  runningContainerCount,
  processLabel,
  DOCKER_STATUS_INFO,
  dockerStatusInfo,
  containerHealthLabel,
  containerComposeLabel,
} from "../assets/js/core/ports.js";

test("formatPublishedPort: published port renders host:port→private/type", () => {
  assert.equal(formatPublishedPort({ ip: "0.0.0.0", private_port: 80, public_port: 8080, type: "tcp" }), "0.0.0.0:8080→80/tcp");
});

test("formatPublishedPort: missing ip defaults to 0.0.0.0", () => {
  assert.equal(formatPublishedPort({ private_port: 80, public_port: 8080, type: "tcp" }), "0.0.0.0:8080→80/tcp");
});

test("formatPublishedPort: unpublished (no public_port) shows private port only", () => {
  assert.equal(formatPublishedPort({ private_port: 5432, type: "tcp" }), "5432/tcp");
  assert.equal(formatPublishedPort({ private_port: 5432, public_port: 0, type: "tcp" }), "5432/tcp");
});

test("formatPublishedPort: defaults type to tcp when absent", () => {
  assert.equal(formatPublishedPort({ private_port: 53, public_port: 53 }), "0.0.0.0:53→53/tcp");
});

test("formatListeningAddress: IPv4", () => {
  assert.equal(formatListeningAddress({ ip: "0.0.0.0", port: 8090 }), "0.0.0.0:8090");
  assert.equal(formatListeningAddress({ ip: "127.0.0.1", port: 22 }), "127.0.0.1:22");
});

test("formatListeningAddress: IPv6 gets bracketed", () => {
  assert.equal(formatListeningAddress({ ip: "::", port: 8090 }), "[::]:8090");
  assert.equal(formatListeningAddress({ ip: "::1", port: 22 }), "[::1]:22");
});

test("formatListeningAddress: empty ip defaults to 0.0.0.0", () => {
  assert.equal(formatListeningAddress({ ip: "", port: 80 }), "0.0.0.0:80");
});

test("sortPorts: orders by proto, then port, then ip; does not mutate input", () => {
  const input = [
    { proto: "udp", ip: "0.0.0.0", port: 53 },
    { proto: "tcp", ip: "0.0.0.0", port: 8090 },
    { proto: "tcp", ip: "127.0.0.1", port: 22 },
    { proto: "tcp", ip: "0.0.0.0", port: 22 },
  ];
  const sorted = sortPorts(input);
  assert.deepEqual(
    sorted.map((p) => `${p.proto}:${p.ip}:${p.port}`),
    ["tcp:0.0.0.0:22", "tcp:127.0.0.1:22", "tcp:0.0.0.0:8090", "udp:0.0.0.0:53"],
  );
  assert.equal(input[0].proto, "udp"); // unmutated
});

test("sortPorts: handles empty/undefined", () => {
  assert.deepEqual(sortPorts([]), []);
  assert.deepEqual(sortPorts(undefined), []);
});

test("sortContainers: running first, then by name; does not mutate input", () => {
  const input = [
    { state: "exited", name: "b-stopped" },
    { state: "running", name: "z-running" },
    { state: "running", name: "a-running" },
  ];
  const sorted = sortContainers(input);
  assert.deepEqual(sorted.map((c) => c.name), ["a-running", "z-running", "b-stopped"]);
  assert.equal(input[0].state, "exited"); // unmutated
});

test("runningContainerCount", () => {
  assert.equal(runningContainerCount([{ state: "running" }, { state: "exited" }, { state: "running" }]), 2);
  assert.equal(runningContainerCount([]), 0);
  assert.equal(runningContainerCount(undefined), 0);
});

test("processLabel: known process with pid", () => {
  assert.equal(processLabel({ process: "nginx", pid: 1234 }), "nginx (1234)");
});

test("processLabel: known process without pid falls back to name only", () => {
  assert.equal(processLabel({ process: "nginx", pid: 0 }), "nginx");
});

test("processLabel: unknown process (unprivileged agent)", () => {
  assert.equal(processLabel({ process: "", pid: 0 }), "unknown");
  assert.equal(processLabel({}), "unknown");
});

test("DOCKER_STATUS_INFO covers every models.DockerStatus value", () => {
  assert.deepEqual(Object.keys(DOCKER_STATUS_INFO).sort(), ["error", "ok", "permission_denied", "unavailable", "unsupported"].sort());
});

test("dockerStatusInfo: permission_denied carries the exact fix command", () => {
  const info = dockerStatusInfo("permission_denied");
  assert.match(info.fixCommand, /install-agent\.sh/);
  assert.match(info.fixCommand, /--docker/);
});

test("dockerStatusInfo: unavailable/error/unsupported have no single fix command", () => {
  assert.equal(dockerStatusInfo("unavailable").fixCommand, null);
  assert.equal(dockerStatusInfo("error").fixCommand, null);
  assert.equal(dockerStatusInfo("unsupported").fixCommand, null);
});

test("dockerStatusInfo: unknown status degrades gracefully", () => {
  const info = dockerStatusInfo("something_new");
  assert.equal(info.title, "Docker status unknown");
  assert.equal(info.fixCommand, null);
});

test("containerHealthLabel", () => {
  assert.equal(containerHealthLabel("healthy"), "Healthy");
  assert.equal(containerHealthLabel("unhealthy"), "Unhealthy");
  assert.equal(containerHealthLabel(""), "");
  assert.equal(containerHealthLabel(undefined), "");
});

test("containerComposeLabel", () => {
  assert.equal(containerComposeLabel({ compose_project: "myapp", compose_service: "web" }), "myapp / web");
  assert.equal(containerComposeLabel({ compose_project: "myapp" }), "myapp");
  assert.equal(containerComposeLabel({ compose_service: "web" }), "web");
  assert.equal(containerComposeLabel({}), "");
});
