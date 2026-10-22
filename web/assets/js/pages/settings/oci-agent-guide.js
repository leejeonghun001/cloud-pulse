// settings/oci-agent-guide.js — Settings → Billing's OCI instance
// principal guide: an agent on an OCI instance queries the Usage API
// itself (CP_OCI_BILLING=instance_principal) for hubs that can't run the
// OCI CLI. The OCID fields only fill in the commands; nothing is saved.
import { el } from "../../ui/components.js";
import { copyToClipboard } from "../../core/clipboard.js";
import { t } from "../../core/i18n.js";

const OCID_PATTERN = /^ocid1\.[a-z0-9]+\.[a-z0-9._:-]+$/i;
const GROUP_PATTERN = /^[A-Za-z0-9_-]{1,100}$/;
const DEFAULT_GROUP = "CostReader";

/** fieldValue returns the trimmed input value when it matches pattern,
 * else the placeholder, so a typo never lands in a copied command. */
function fieldValue(input, pattern, placeholder) {
  const v = input.value.trim();
  return pattern.test(v) ? v : placeholder;
}

/** STEPS lists the guide in order. Each code() builds the step's
 * copyable text from the current field values. */
const STEPS = [
  {
    key: "group",
    code: ({ instance }) => `instance.id = '${instance}'`,
  },
  {
    key: "policy",
    code: ({ group }) => [
      `Allow dynamic-group ${group} to read usage-reports in tenancy`,
      `Allow dynamic-group ${group} to read metrics in tenancy`,
    ].join("\n"),
  },
  {
    key: "cli",
    code: () => [
      "curl -fsSL https://raw.githubusercontent.com/oracle/oci-cli/master/scripts/install/install.sh -o /tmp/oci-install.sh",
      "sudo bash /tmp/oci-install.sh --accept-all-defaults \\",
      "  --install-dir /opt/oci-cli --exec-dir /usr/local/bin --script-dir /opt/oci-cli/scripts",
      "oci --version",
    ].join("\n"),
  },
  {
    key: "test",
    code: ({ tenancy }) => [
      "sudo -u cloud-pulse env HOME=/tmp oci usage-api usage-summary request-summarized-usages \\",
      "  --auth instance_principal \\",
      `  --tenant-id "${tenancy}" \\`,
      '  --time-usage-started "$(date -u +%Y-%m-01T00:00:00Z)" \\',
      '  --time-usage-ended "$(date -u -d tomorrow +%Y-%m-%dT00:00:00Z)" \\',
      "  --granularity DAILY --query-type COST",
    ].join("\n"),
  },
  {
    key: "env",
    code: ({ tenancy }) => [
      "sudo tee -a /etc/cloud-pulse/agent.env >/dev/null <<'EOF'",
      "CP_OCI_BILLING=instance_principal",
      `CP_OCI_TENANCY_ID=${tenancy}`,
      "EOF",
      "sudo systemctl restart cloud-pulse-agent",
      'sudo journalctl -u cloud-pulse-agent -n 50 --no-pager | grep "oci billing"',
    ].join("\n"),
  },
  { key: "hub" },
];

/**
 * textField builds a labelled text input with a hint line.
 * @returns {{node: HTMLElement, input: HTMLInputElement}}
 */
function textField({ id, label, hint, value = "", example = "" }) {
  const node = el("div", { class: "cp-field" });
  node.append(el("label", { class: "cp-label", attrs: { for: id }, text: label }));
  const input = /** @type {HTMLInputElement} */ (
    el("input", {
      class: "cp-input",
      attrs: { id, type: "text", value, placeholder: example, spellcheck: "false", autocomplete: "off", "aria-describedby": `${id}-hint` },
    })
  );
  node.append(input, el("p", { class: "cp-muted-small", attrs: { id: `${id}-hint` }, text: hint }));
  return { node, input };
}

/**
 * ociAgentGuideCard builds the guide card.
 * @param {Object} opts
 * @param {Object|undefined} opts.oci the OCI models.CloudCostSnapshot, if any
 * @param {boolean} [opts.bare] content only, without the card and title
 * @returns {HTMLElement}
 */
export function ociAgentGuideCard({ oci, bare = false }) {
  // bare: the guide's content only, for embedding in Settings → Billing's
  // OCI section (which already names the provider and its accounts).
  const card = bare
    ? el("div", { class: "cp-oci-guide", attrs: { id: "oci-agent" } })
    : el("section", { class: "cp-card cp-billing-card cp-oci-guide", attrs: { id: "oci-agent", "aria-labelledby": "cp-oci-guide-heading" } });
  if (!bare) card.append(el("h2", { class: "cp-card-title", text: t("billing.oci_agent_title"), attrs: { id: "cp-oci-guide-heading" } }));
  card.append(el("p", { class: "cp-muted-small", text: t("billing.oci_agent_intro") }));
  if (!bare) {
    card.append(el("p", {
      class: "cp-oci-guide-state",
      text: oci?.source_host ? t("billing.oci_agent_active", { host: oci.source_host }) : t("billing.oci_agent_inactive"),
    }));
  }

  const fields = el("div", { class: "cp-oci-guide-fields" });
  const tenancy = textField({ id: "cp-oci-tenancy", label: t("billing.oci_agent_tenancy_label"), hint: t("billing.oci_agent_tenancy_hint"), example: "ocid1.tenancy.oc1..." });
  const instance = textField({ id: "cp-oci-instance", label: t("billing.oci_agent_instance_label"), hint: t("billing.oci_agent_instance_hint"), example: "ocid1.instance.oc1..." });
  const group = textField({ id: "cp-oci-group", label: t("billing.oci_agent_group_label"), hint: t("billing.oci_agent_group_hint"), value: DEFAULT_GROUP });
  fields.append(tenancy.node, instance.node, group.node);
  card.append(fields);

  const { list, copyStatus, sync } = buildGuideSteps(STEPS, "billing.oci_agent_step", () => ({
    tenancy: fieldValue(tenancy.input, OCID_PATTERN, "<tenancy-ocid>"),
    instance: fieldValue(instance.input, OCID_PATTERN, "<instance-ocid>"),
    group: fieldValue(group.input, GROUP_PATTERN, DEFAULT_GROUP),
  }));
  for (const f of [tenancy, instance, group]) f.input.addEventListener("input", sync);
  sync();

  card.append(list, copyStatus);
  return card;
}

/**
 * buildGuideSteps renders a numbered setup guide: each step's title and
 * body come from `${keyPrefix}_${step.key}_title|_body`, and a step with
 * code() gets a copyable block filled from values(). Call sync() after
 * values change.
 * @param {Array<{key: string, code?: (v: Object) => string}>} steps
 * @param {string} keyPrefix
 * @param {() => Object} values
 * @returns {{list: HTMLElement, copyStatus: HTMLElement, sync: () => void}}
 */
export function buildGuideSteps(steps, keyPrefix, values) {
  const copyStatus = el("p", { class: "cp-muted-small cp-oci-guide-copy-status", attrs: { role: "status", "aria-live": "polite" } });
  const list = el("ol", { class: "cp-oci-guide-steps" });
  /** @type {Array<() => void>} */
  const syncers = [];
  for (const step of steps) {
    const item = el("li", { class: "cp-oci-guide-step" });
    const title = t(`${keyPrefix}_${step.key}_title`);
    item.append(el("h3", { class: "cp-oci-guide-step-title", text: title }));
    item.append(el("p", { class: "cp-muted-small", text: t(`${keyPrefix}_${step.key}_body`) }));
    if (step.code) {
      const code = el("code", { text: "" });
      const pre = el("pre", { class: "cp-code-block cp-code-block-wrap", children: [code] });
      const copy = el("button", {
        class: "cp-btn cp-btn-secondary cp-oci-guide-copy",
        attrs: { type: "button", "aria-label": t("billing.oci_agent_copy_label", { step: title }) },
        text: t("common.copy"),
      });
      copy.addEventListener("click", async () => {
        const result = await copyToClipboard(code.textContent || "");
        copyStatus.textContent = result === "copied" ? t("billing.oci_agent_copied") : t("billing.oci_agent_copy_failed");
      });
      item.append(el("div", { class: "cp-oci-guide-code", children: [pre, copy] }));
      syncers.push(() => { code.textContent = step.code(values()); });
    }
    list.append(item);
  }
  const sync = () => syncers.forEach((fn) => fn());
  sync();
  return { list, copyStatus, sync };
}
