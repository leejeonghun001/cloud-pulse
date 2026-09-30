// ports.js — pure helpers for SPEC-v0.5 §D inventory UI (host detail
// "Services & ports" card, overview "Containers" column): port-chip
// formatting, container/port sorting, and the exact remediation copy
// for each models.DockerStatus. No DOM access here so this module is
// fully covered by node --test without a browser.
import { t } from "./i18n.js";

/**
 * formatPublishedPort renders one models.ContainerPort as the
 * SPEC-v0.5 §D chip text, e.g. "0.0.0.0:8080→80/tcp". A port with no
 * PublicPort (container-internal only, not published to the host) is
 * rendered as "80/tcp" (private port + type only, no arrow).
 * @param {{ip?: string, private_port: number, public_port?: number, type: string}} port
 * @returns {string}
 */
export function formatPublishedPort(port) {
  const type = port.type || "tcp";
  if (!port.public_port) {
    return `${port.private_port}/${type}`;
  }
  const ip = port.ip && port.ip !== "" ? port.ip : "0.0.0.0";
  return `${ip}:${port.public_port}→${port.private_port}/${type}`;
}

/**
 * formatListeningAddress renders one models.ListeningPort's address as
 * "ip:port", e.g. "0.0.0.0:8090" or "[::]:8090" for an IPv6 wildcard
 * (bracketed per URL/host convention when the address contains a
 * colon).
 * @param {{ip: string, port: number}} p
 * @returns {string}
 */
export function formatListeningAddress(p) {
  const ip = p.ip && p.ip !== "" ? p.ip : "0.0.0.0";
  return ip.includes(":") ? `[${ip}]:${p.port}` : `${ip}:${p.port}`;
}

/**
 * sortPorts sorts a models.ListeningPort[] by proto then port then ip,
 * for deterministic table rendering. Returns a new array; does not
 * mutate the input.
 * @param {Array<{proto: string, ip: string, port: number}>} ports
 * @returns {Array}
 */
export function sortPorts(ports) {
  return [...(ports || [])].sort((a, b) => {
    if (a.proto !== b.proto) return a.proto < b.proto ? -1 : 1;
    if (a.port !== b.port) return a.port - b.port;
    return a.ip < b.ip ? -1 : a.ip > b.ip ? 1 : 0;
  });
}

/**
 * sortContainers sorts a models.Container[] by running-first, then
 * name, for deterministic table rendering. Returns a new array; does
 * not mutate the input.
 * @param {Array<{state: string, name: string}>} containers
 * @returns {Array}
 */
export function sortContainers(containers) {
  return [...(containers || [])].sort((a, b) => {
    const aRunning = a.state === "running" ? 0 : 1;
    const bRunning = b.state === "running" ? 0 : 1;
    if (aRunning !== bRunning) return aRunning - bRunning;
    return a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
  });
}

/**
 * runningContainerCount counts containers whose state is "running".
 * @param {Array<{state: string}>} containers
 * @returns {number}
 */
export function runningContainerCount(containers) {
  return (containers || []).filter((c) => c.state === "running").length;
}

/**
 * processLabel renders a listening port's owning process for display,
 * falling back to "unknown" (agent ran unprivileged and couldn't
 * resolve it — models.ListeningPort.Process is "" in that case).
 * @param {{process?: string, pid?: number}} p
 * @returns {string}
 */
export function processLabel(p) {
  if (!p.process) return t("host.process_unknown");
  return p.pid ? `${p.process} (${p.pid})` : p.process;
}

/**
 * DOCKER_STATUS_INFO maps each models.DockerStatus value to display
 * copy for the host detail Services & ports card's empty/error states:
 * a short title, a longer message, and — for permission_denied — the
 * exact fix command from SPEC-v0.5 §C (install-agent.sh --docker).
 * unavailable/error/unsupported have no fix command (null) since no
 * single command resolves them (no daemon running, a transient error,
 * or an unimplemented platform, respectively).
 *
 * A getter (rather than a plain object literal) so its title/message
 * strings re-resolve against the current language on every read, since
 * this module's exports are evaluated once at import time.
 * @returns {Object<string, {title: string, message: string, fixCommand: string|null}>}
 */
export const DOCKER_STATUS_INFO = {
  get ok() {
    return { title: "Docker", message: "", fixCommand: null };
  },
  get unavailable() {
    return { title: t("host.docker_not_detected"), message: t("host.docker_not_detected_message"), fixCommand: null };
  },
  get permission_denied() {
    return {
      title: t("host.docker_access_denied"),
      message: t("host.docker_access_denied_message"),
      fixCommand: "curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh | sudo bash -s -- --reinstall --docker",
    };
  },
  get error() {
    return { title: t("host.docker_collection_failed"), message: t("host.docker_collection_failed_message"), fixCommand: null };
  },
  get unsupported() {
    return { title: t("host.docker_not_supported"), message: t("host.docker_not_supported_message"), fixCommand: null };
  },
};

/**
 * dockerStatusInfo looks up DOCKER_STATUS_INFO by status, falling back
 * to a generic "unknown status" entry so an unrecognized future value
 * never crashes the card.
 * @param {string} status
 * @returns {{title: string, message: string, fixCommand: string|null}}
 */
export function dockerStatusInfo(status) {
  return DOCKER_STATUS_INFO[status] || { title: t("host.docker_status_unknown"), message: String(status), fixCommand: null };
}

/**
 * containerHealthLabel renders a container's health for display,
 * capitalizing the first letter ("healthy" -> "Healthy"), or "" when
 * the container has no healthcheck (models.Container.Health == "").
 * @param {string} [health]
 * @returns {string}
 */
export function containerHealthLabel(health) {
  if (!health) return "";
  return health.charAt(0).toUpperCase() + health.slice(1);
}

/**
 * containerComposeLabel renders a container's compose project/service
 * for display, e.g. "myapp / web", or "" when the container wasn't
 * created by Compose (both fields empty).
 * @param {{compose_project?: string, compose_service?: string}} container
 * @returns {string}
 */
export function containerComposeLabel(container) {
  const project = container.compose_project || "";
  const service = container.compose_service || "";
  if (!project && !service) return "";
  if (project && service) return `${project} / ${service}`;
  return project || service;
}
