// refresh.js — pure refresh-cycle control flow shared by dashboard pages.

/**
 * runHostDetailRefresh loads a host detail summary and, while the same page
 * remains active, its metrics and next refresh. A missing summary returns
 * false from loadSummary and intentionally terminates the refresh chain.
 *
 * @param {{isActive: () => boolean, loadSummary: () => Promise<boolean>, loadMetrics: () => Promise<void>, rescheduleOnly: boolean, schedule: () => void}} options
 * @returns {Promise<void>}
 */
export async function runHostDetailRefresh({ isActive, loadSummary, loadMetrics, rescheduleOnly, schedule }) {
  if (!isActive()) return;
  const summaryAvailable = await loadSummary();
  if (!summaryAvailable || !isActive()) return;
  if (!rescheduleOnly) await loadMetrics();
  if (isActive()) schedule();
}
