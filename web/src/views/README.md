# Portal views

A **view** is how the challenge board is drawn. The standard board is one; the globe is
another. A view is not a theme — `theme/` owns the colour scheme, and every theme keeps
working inside every view.

Only `/challenges` has views. A challenge's own page, the scoreboard and the admin console
are the same in all of them.

## Adding a view (one entry + one module)

1. Write `<name>/<Name>Board.tsx` exporting a component that takes `PortalViewProps`. It
   renders the whole board — the page head included — and gets `selectView` so it can offer
   a way out to another view.
2. Add an entry to `VIEWS` in `registry.ts`: an `id` (the value `portal_view` carries on the
   wire), a `label`, a one-line `description`, and a `load` that dynamically imports the
   module. Optionally a cheap `Fallback` to show while that chunk downloads.

That is the whole contract. No route change, no switch statement, no stylesheet wiring. The
resolver, the persistence and the outlet pick it up from the registry, and the dynamic
import means the new view's cost lands in the new view's chunk.

## Resolution order

`resolvePortalView` (`resolve.ts`) is a pure function applying, in order:

1. **Player preference** — `localStorage["flagfish.portalView"]`, a view id. This is the
   opt-out: a player who wants the plain board back sets `standard` and keeps it.
2. **Instance** — `portal_view` from the admin config, served on `GET /instance`.
3. **The standard board** — the one view that needs nothing but a DOM.

An unknown *preference* is dropped in silence: it is one stale value in one browser, and the
next source is the right answer. An unknown *instance* view is not silent — it is a setting
an operator made for everybody and it is not being honoured, so the resolver reports it and
`usePortalView` warns once on the console. Neither case renders nothing.

## Degrading

A view that cannot run has to say so and then get out of the way. The globe checks for a
WebGL context up front (`globe/webgl.ts`) and, when there is none, explains itself and
renders the standard board in place — a player is never left looking at a blank canvas, and
never loses access to challenges because their browser is old.
