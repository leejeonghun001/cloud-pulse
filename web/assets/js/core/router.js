// router.js — hash router with an auth guard: every route except
// #/login requires a known-authenticated session (checked via the
// shell's cached auth state, not a fetch per navigation); an
// unauthenticated visit to a guarded route redirects to #/login with a
// return-to ("next") param, restored after a successful login.
import { isLoginRoute, buildLoginRedirect } from "./auth.js";

/**
 * @typedef {Object} Route
 * @property {RegExp} pattern
 * @property {(match: RegExpMatchArray, query: URLSearchParams) => void} handler
 * @property {boolean} [public] true for routes reachable without auth (login)
 */

export class Router {
  /**
   * @param {Object} opts
   * @param {() => boolean} opts.isAuthenticated returns whether the
   *   current session is known-valid (cached shell state).
   * @param {() => void} [opts.onGuardRedirect] called when a guarded
   *   route redirects to login (e.g. to close any open overlays).
   */
  constructor({ isAuthenticated, onGuardRedirect }) {
    this.routes = /** @type {Route[]} */ ([]);
    this.isAuthenticated = isAuthenticated;
    this.onGuardRedirect = onGuardRedirect || (() => {});
    this.notFoundHandler = null;
  }

  /**
   * add registers a route.
   * @param {RegExp} pattern must match the full hash including query,
   *   e.g. /^#\/host\/([^/?]+)$/ — query-bearing routes should match up
   *   to an optional "?" themselves.
   * @param {(match: RegExpMatchArray, query: URLSearchParams) => void} handler
   * @param {{public?: boolean}} [opts]
   */
  add(pattern, handler, opts = {}) {
    this.routes.push({ pattern, handler, public: Boolean(opts.public) });
  }

  /** setNotFound registers a fallback handler for an unmatched hash. */
  setNotFound(handler) {
    this.notFoundHandler = handler;
  }

  /**
   * resolve finds the matching route (and query params) for a hash
   * string, without applying the auth guard or invoking any handler.
   * Exposed for testing the matching logic in isolation.
   * @param {string} hash
   * @returns {{route: Route, match: RegExpMatchArray, query: URLSearchParams}|null}
   */
  resolve(hash) {
    const qIdx = hash.indexOf("?");
    const path = qIdx === -1 ? hash : hash.slice(0, qIdx);
    const query = new URLSearchParams(qIdx === -1 ? "" : hash.slice(qIdx + 1));
    for (const route of this.routes) {
      const match = path.match(route.pattern) || hash.match(route.pattern);
      if (match) return { route, match, query };
    }
    return null;
  }

  /**
   * dispatch resolves the given hash (defaulting to location.hash) and
   * invokes its handler, applying the auth guard first: a guarded route
   * visited without an authenticated session redirects to #/login?next=
   * instead of running the handler.
   * @param {string} [hash]
   */
  dispatch(hash = globalThis.location.hash || "#/") {
    if (!hash || hash === "#") hash = "#/";

    const resolved = this.resolve(hash);
    const authed = this.isAuthenticated();

    if (resolved && resolved.route.public) {
      // The login route itself: if already authenticated, bounce to
      // the requested return-to target (or "/") instead of showing the
      // login form again.
      if (authed && isLoginRoute(hash)) {
        const next = resolved.query.get("next");
        this.navigate(next && !isLoginRoute(next) ? next : "#/");
        return;
      }
      resolved.route.handler(resolved.match, resolved.query);
      return;
    }

    if (!authed) {
      this.onGuardRedirect();
      this.navigate(buildLoginRedirect(hash));
      return;
    }

    if (resolved) {
      resolved.route.handler(resolved.match, resolved.query);
      return;
    }

    if (this.notFoundHandler) {
      this.notFoundHandler();
    }
  }

  /**
   * navigate sets location.hash to target. If target already equals
   * the current hash, dispatch() is called directly instead (setting
   * an unchanged hash doesn't fire "hashchange").
   * @param {string} target
   */
  navigate(target) {
    if (globalThis.location.hash === target) {
      this.dispatch(target);
      return;
    }
    globalThis.location.hash = target;
  }

  /** start wires the hashchange listener and performs the initial dispatch. */
  start() {
    globalThis.addEventListener("hashchange", () => this.dispatch());
    this.dispatch();
  }
}
