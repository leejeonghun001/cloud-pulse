// settings-sections.js — route-independent metadata for Settings links.
// The mount functions remain in pages/settings/index.js, which is lazy.
import { t } from "./i18n.js";

export const SETTINGS_SECTIONS = [
  { id: "general", get label() { return t("settings.section_general"); }, iconName: "settings" },
  { id: "network", get label() { return t("settings.section_network"); }, iconName: "network" },
  { id: "security", get label() { return t("settings.section_security"); }, iconName: "shield" },
  { id: "agents", get label() { return t("settings.section_agents"); }, iconName: "server" },
  { id: "traffic", get label() { return t("settings.section_traffic"); }, iconName: "gauge" },
  { id: "notifications", get label() { return t("settings.section_notifications"); }, iconName: "bell" },
  { id: "alert-rules", get label() { return t("settings.section_alert_rules"); }, iconName: "triangleAlert" },
  { id: "billing", get label() { return t("settings.section_billing"); }, iconName: "cloud" },
  { id: "storage", get label() { return t("settings.section_storage"); }, iconName: "hardDrive" },
];
