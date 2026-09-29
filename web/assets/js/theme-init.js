// theme-init.js — classic (non-module) script loaded synchronously in
// <head>, before first paint, so the correct .dark class is present on
// <html> before any content renders (avoids a flash of the wrong
// theme). Deliberately tiny and dependency-free: this file cannot
// import theme.js (an ES module) because module scripts are deferred
// until after the DOM is parsed, defeating the "before first paint"
// requirement. theme.js re-implements the same read/apply logic for
// use after bootstrap (theme toggle clicks, "system" preference
// changes) and is the source of truth for that logic; keep the two in
// sync if the storage key or resolution rule ever changes.
(function () {
  "use strict";
  var STORAGE_KEY = "cp_theme";
  var theme = "system";
  try {
    var stored = window.localStorage.getItem(STORAGE_KEY);
    if (stored === "dark" || stored === "light" || stored === "system") {
      theme = stored;
    }
  } catch (e) {
    // localStorage unavailable (private mode, disabled storage): fall
    // back to "system".
  }

  var isDark;
  if (theme === "dark") {
    isDark = true;
  } else if (theme === "light") {
    isDark = false;
  } else {
    isDark = !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
  }

  var root = document.documentElement;
  if (isDark) {
    root.classList.add("dark");
  } else {
    root.classList.remove("dark");
  }
})();
