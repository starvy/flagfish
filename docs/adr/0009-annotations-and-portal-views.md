# ADR-0009: Challenge annotations and portal views

- **Status:** Accepted
- **Date:** 2026-07-28

## Context

The player portal renders the challenge board one way: a grid grouped by category. We wanted an
organizer to be able to choose a different presentation — the first being a WebGL globe that places
each challenge in a country — without the alternate presentation becoming a fork of the portal, a
plugin (ADR-0007), or a pile of columns on `challenges` that only one renderer understands.

Two questions had to be settled together: **where does a presentation get its placement data**, and
**who decides which presentation an instance uses**.

## Options considered

For the data:

- **(a) Columns on `challenges`** — `country char(2)` today, another column per future view.
- **(b) Reuse `tags`** — encode `country:CZ` into the existing free-string tag set.
- **(c) A keyed annotation table** — `challenge_annotations(challenge_id, key, value)`,
  `UNIQUE (challenge_id, key)`, with well-known keys validated harder.

For the switch:

- **(d) A player-side choice of themes** — every player picks their own presentation.
- **(e) An instance-wide config key with a player opt-out.**

## Decision

**(c) and (e).**

Annotations are a second table, not a reuse of `tags`, because the two answer different questions.
A tag is membership — a challenge either carries `"web"` or it does not. An annotation is a lookup
**by name**: a renderer asks *"what is this challenge's `country`"* and needs one answer, which is
exactly what `UNIQUE (challenge_id, key)` makes the database enforce. Encoding key-value pairs into
free strings (b) would have made the schema lie about the data's shape; per-view columns (a) would
have made every new view a migration.

The key namespace is open — nothing in the schema knows what `country` means — but the shape is
constraint-backed: key charset, value length, and a `CHECK` that a `country` value is uppercase
ISO 3166-1 alpha-2. The full code list lives in `internal/domain/geo` (it changes when the world
does, not when the schema does); the *shape* is a fact the database holds, so no write path can
store a value a renderer would trip over. Annotations on locked or masked challenges are stripped
to `{}` in the read model: where a challenge sits on a map is a clue about what it is.

The active view is one config key, `ctf_portal_view`, a **closed set** declared once in
`internal/config`. A view is a value the client has code for, so an unknown spelling is not
forward-compatible extension — it is a portal that renders nothing, and it is refused at the write.
Players can opt back to the standard board per browser; the server does not know or care. On the
client, a view is one registry entry (`web/src/views/registry.ts`) loading a lazy chunk — adding a
view touches the registry, the config set, and nothing else.

Live movement on the globe (solve pulses) rides a broadcast SSE stream published inside the solve
transaction — correct-flag path only, so the hot path's wrong-answer leg (ADR-0006) never pays for
it — and each event is dropped at delivery while the scoreboard is frozen, the same property the
first-blood webhook honours. The payload names a challenge, never an account or team.

## What was given up

- **Arbitrary per-player theming.** The organizer curates the event's look; a player's only choice
  is the standard board. Player-side theme galleries are a different product.
- **Free-form annotation semantics in the database.** The schema pins shape, not meaning; a typo'd
  key (`countr`) is accepted as an unknown key rather than rejected. The admin UI's pickers, not
  the schema, keep operators on the well-known keys.
- **Replayable solve events.** The feed has no history and advertises none (no SSE `id:`); a
  reconnecting client just misses pulses. Anything that needs the record reads the solves table.
- **CTFd import of placements.** CTFd has no country concept, so imported instances arrive
  unplaced and are annotated by hand afterwards.

## Addendum (2026-07-28): a view may carry a skin

The globe shipped as a dark scene inside whatever page the colour theme had painted. On a light
instance that is a black box in a white document — two things, not one — and the separation the
original decision drew ("a view is how the board is drawn, a theme is what colour everything is")
turned out to be too clean to describe a view that is a scene rather than a document.

A `PortalView` gains two optional fields, both of them declarative and both of them read outside
the view:

- `theme` — the name of a colour theme this view brings with it.
- `chrome: "immersive"` — the board wants the viewport rather than a column, so the shell drops
  its width and padding and floats the header and the clock banners over the content. Only on the
  board route; a challenge's own page, the scoreboard and the admin console stay documents.

`theme` sits at the **top** of the theme resolution order — above the player's saved colour choice
and the instance's `ctf_theme`. A view is a whole presentation, and a presentation that owns the
board but not the header is half a design. The name is resolved through the same registry as every
other source, so a view naming a theme this build does not ship falls through harmlessly instead of
failing. `views/` still knows nothing about `theme/`: a view names a theme as a string, and the
resolver in `theme/` is the only thing that looks it up.

The escape hatch does not change: the player's opt-out to the standard board is still the whole of
it, and taking the standard board back takes its palette with it.

### What was given up

- **Keeping your own colours under a themed view.** While the globe is the resolved view, a player
  who picked `light` gets `nocturne`. The saved preference is not lost, and the theme picker in
  settings says why it is not what is on screen — but the only way to see it again is the switch
  back to the list view.
- **Partial theming.** A view carries one theme name for the whole document or none at all; there
  is no "this view repaints the board but not the shell". That was the state we were fixing.
- **A shell that is only ever a shell.** The layout now has a second shape, and a view can ask for
  it. The shape is one attribute and one grid, not a second layout tree, but the shell is no longer
  ignorant of what it contains.
