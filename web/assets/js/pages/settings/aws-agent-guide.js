// settings/aws-agent-guide.js — Settings → Billing's AWS guide: an agent
// on an EC2 instance queries Cost Explorer with the instance's IAM role
// (CP_AWS_BILLING=instance_role), so no access key is stored anywhere.
// One agent per AWS account; each account shows up separately.
import { el } from "../../ui/components.js";
import { t } from "../../core/i18n.js";
import { buildGuideSteps } from "./oci-agent-guide.js";

const STEPS = [
  {
    key: "role",
    code: () => JSON.stringify({
      Version: "2012-10-17",
      Statement: [{ Effect: "Allow", Action: ["ce:GetCostAndUsage", "ce:GetCostForecast"], Resource: "*" }],
    }, null, 2),
  },
  {
    key: "cli",
    code: () => [
      'curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-$(uname -m).zip" -o /tmp/awscliv2.zip',
      "cd /tmp && unzip -qo awscliv2.zip && sudo ./aws/install --update",
      "aws --version",
    ].join("\n"),
  },
  {
    key: "test",
    code: () => [
      "sudo -u cloud-pulse env HOME=/tmp AWS_DEFAULT_REGION=us-east-1 aws sts get-caller-identity",
      "sudo -u cloud-pulse env HOME=/tmp AWS_DEFAULT_REGION=us-east-1 aws ce get-cost-and-usage \\",
      '  --time-period Start=$(date -u +%Y-%m-01),End=$(date -u -d tomorrow +%Y-%m-%d) \\',
      "  --granularity MONTHLY --metrics UnblendedCost",
    ].join("\n"),
  },
  {
    key: "env",
    code: () => [
      "sudo tee -a /etc/cloud-pulse/agent.env >/dev/null <<'EOF'",
      "CP_AWS_BILLING=instance_role",
      "EOF",
      "sudo systemctl restart cloud-pulse-agent",
      'sudo journalctl -u cloud-pulse-agent -n 50 --no-pager | grep "aws billing"',
    ].join("\n"),
  },
  { key: "hub" },
];

/**
 * awsAgentGuideCard builds the AWS instance-role guide card.
 * @param {Object} opts
 * @param {Object|undefined} opts.aws an agent-reported AWS snapshot, if any
 * @returns {HTMLElement}
 */
export function awsAgentGuideCard({ aws }) {
  const card = el("section", { class: "cp-card cp-billing-card cp-oci-guide", attrs: { id: "aws-agent", "aria-labelledby": "cp-aws-guide-heading" } });
  card.append(el("h2", { class: "cp-card-title", text: t("billing.aws_agent_title"), attrs: { id: "cp-aws-guide-heading" } }));
  card.append(el("p", { class: "cp-muted-small", text: t("billing.aws_agent_intro") }));
  card.append(el("p", {
    class: "cp-oci-guide-state",
    text: aws?.source_host ? t("billing.aws_agent_active", { host: aws.source_host }) : t("billing.aws_agent_inactive"),
  }));
  const { list, copyStatus } = buildGuideSteps(STEPS, "billing.aws_agent_step", () => ({}));
  card.append(list, copyStatus);
  return card;
}
