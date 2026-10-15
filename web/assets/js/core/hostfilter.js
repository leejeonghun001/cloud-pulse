// hostfilter.js — pure facet filtering for the overview's "All Systems"
// card (status + provider chips). Every facet is derived from fields the
// hub already reports in HostSummary; nothing is estimated.

export const STATUS_FACETS = ["all", "online", "offline", "attention"];

const ALERT_LEVELS = new Set(["warning", "critical", "exceeded"]);
const KNOWN_PROVIDERS = new Set(["aws", "oci"]);

/** providerKey folds unknown/empty providers into "other", matching providerBadge. */
export function providerKey(row) {
  const p = row?.host?.provider;
  return KNOWN_PROVIDERS.has(p) ? p : "other";
}

/**
 * hostNeedsAttention: offline, or a reported monthly traffic level
 * (outbound or inbound) at warning or worse. Agent updates are not
 * counted — an outdated agent is not an operational problem.
 */
export function hostNeedsAttention(row) {
  if (row?.status === "down") return true;
  return ALERT_LEVELS.has(row?.egress?.level) || ALERT_LEVELS.has(row?.egress?.rx_level);
}

export function matchesStatusFacet(row, status) {
  switch (status) {
    case "online": return row?.status === "up";
    case "offline": return row?.status === "down";
    case "attention": return hostNeedsAttention(row);
    default: return true;
  }
}

/**
 * facetCounts returns chip counts. Status counts ignore the provider
 * selection and provider counts respect the status selection, so each
 * chip shows how many hosts clicking it would leave in view.
 * Provider chips are listed from `fleet` (all hosts, default `rows`) plus
 * any selected provider, so a pressed chip never disappears when the text
 * filter narrows `rows`.
 * @param {Array} rows
 * @param {{status?: string, providers?: string[]}} facets
 * @param {Array} [fleet]
 */
export function facetCounts(rows, facets = {}, fleet = rows) {
  const providers = new Set(facets.providers ?? []);
  const byProvider = (row) => providers.size === 0 || providers.has(providerKey(row));
  const status = Object.fromEntries(STATUS_FACETS.map((key) => [key, rows.filter((r) => byProvider(r) && matchesStatusFacet(r, key)).length]));
  const providerCounts = new Map();
  for (const row of rows) {
    if (!matchesStatusFacet(row, facets.status ?? "all")) continue;
    const key = providerKey(row);
    providerCounts.set(key, (providerCounts.get(key) ?? 0) + 1);
  }
  // Always list providers present in the fleet, even at zero under the
  // current status, so chips don't jump around while filtering.
  const present = [...new Set([...fleet.map(providerKey), ...providers])].sort();
  return { status, providers: present.map((key) => ({ key, count: providerCounts.get(key) ?? 0 })) };
}

/** applyFacets keeps rows matching the status facet and any selected provider. */
export function applyFacets(rows, facets = {}) {
  const providers = new Set(facets.providers ?? []);
  return rows.filter((row) => matchesStatusFacet(row, facets.status ?? "all")
    && (providers.size === 0 || providers.has(providerKey(row))));
}

/** normalizeFacets drops unknown values (e.g. stale stored state). */
export function normalizeFacets(raw) {
  const status = STATUS_FACETS.includes(raw?.status) ? raw.status : "all";
  const providers = Array.isArray(raw?.providers)
    ? [...new Set(raw.providers.filter((p) => p === "aws" || p === "oci" || p === "other"))].sort()
    : [];
  return { status, providers };
}

export function facetsActive(facets) {
  return (facets?.status ?? "all") !== "all" || (facets?.providers?.length ?? 0) > 0;
}
