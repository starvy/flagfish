import { describe, expect, it } from "vitest";
import { resolvePortalView } from "./resolve";
import { DEFAULT_VIEW, VIEWS, viewById } from "./registry";
// The one place the two names meet. Nothing in `views/` imports `theme/` — a view names a theme
// as a string and the resolver in `theme/` looks it up — so a typo here would only show up as an
// app that quietly kept the old palette.
import { themeByName } from "../theme/registry";

// An id no build ships, standing in for a stale preference or a config from a newer server.
const UNKNOWN = "hologram";

describe("resolvePortalView", () => {
  it("takes the player's saved choice when it names a view this build ships", () => {
    const r = resolvePortalView({ preference: DEFAULT_VIEW.id, instanceView: UNKNOWN });
    expect(r.view).toBe(DEFAULT_VIEW);
    expect(r.source).toBe("preference");
    // The instance value was never consulted, so there is nothing to complain about.
    expect(r.unrecognised).toBeNull();
  });

  it("follows the instance when the player has never chosen", () => {
    const r = resolvePortalView({ preference: null, instanceView: "globe" });
    expect(r.view.id).toBe("globe");
    expect(r.source).toBe("instance");
  });

  // The opt-out the globe's own "list view" button writes.
  it("lets a player take the standard board back from an instance on the globe", () => {
    const r = resolvePortalView({ preference: "standard", instanceView: "globe" });
    expect(r.view.id).toBe("standard");
    expect(r.source).toBe("preference");
  });

  it("lands on the default when neither source says anything", () => {
    const r = resolvePortalView({});
    expect(r.view).toBe(DEFAULT_VIEW);
    expect(r.source).toBe("default");
    expect(r.unrecognised).toBeNull();
  });

  it("ignores a stale preference in silence and still honours the instance", () => {
    const r = resolvePortalView({ preference: UNKNOWN, instanceView: DEFAULT_VIEW.id });
    expect(r.view).toBe(DEFAULT_VIEW);
    expect(r.source).toBe("instance");
    expect(r.unrecognised).toBeNull();
  });

  it("falls back from an instance view this build does not ship, and says which", () => {
    const r = resolvePortalView({ preference: null, instanceView: UNKNOWN });
    expect(r.view).toBe(DEFAULT_VIEW);
    expect(r.source).toBe("default");
    expect(r.unrecognised).toBe(UNKNOWN);
  });

  it("treats an empty instance value as unset, not as a broken setting", () => {
    expect(resolvePortalView({ instanceView: "" }).unrecognised).toBeNull();
  });

  // The property that matters most: there is no input for which the board renders nothing.
  it("never returns without a view to render", () => {
    const values = [null, undefined, "", UNKNOWN, ...VIEWS.map((v) => v.id)];
    for (const preference of values) {
      for (const instanceView of values) {
        expect(resolvePortalView({ preference, instanceView }).view).toBeTruthy();
      }
    }
  });
});

describe("view registry", () => {
  it("ids are unique", () => {
    const ids = VIEWS.map((v) => v.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("every view is complete enough to offer and to load", () => {
    for (const view of VIEWS) {
      expect(view.label, `${view.id} has no label`).toBeTruthy();
      expect(view.description, `${view.id} has no description`).toBeTruthy();
      expect(typeof view.load).toBe("function");
    }
  });

  it("the default view is in the registry", () => {
    expect(viewById(DEFAULT_VIEW.id)).toBe(DEFAULT_VIEW);
  });

  it("a view that carries a skin names a theme this build ships", () => {
    for (const view of VIEWS) {
      if (view.theme === undefined) continue;
      expect(themeByName(view.theme), `${view.id} asks for theme "${view.theme}"`).toBeDefined();
    }
  });

  // The wire contract: `portal_view` names a view by this id, and "standard" is the value the
  // server falls back to, so this build must always be able to render it.
  it("ships the standard board", () => {
    expect(viewById("standard")).toBeDefined();
    expect(DEFAULT_VIEW.id).toBe("standard");
  });
});
