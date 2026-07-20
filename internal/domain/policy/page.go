package policy

// PageView is the L2 gate for a CMS page on the public surface. L1 (Decide on ClassPages) has
// already run and gates nothing per-row, because a published public page is readable by anyone —
// the per-page rules live on the row, not on the route class, so they are decided here against it.
//
// Two flags, in order:
//
//   - draft: the page is unpublished. It does not exist to the public — 404, hiding its existence
//     exactly as a hidden challenge does, rather than a 403 that would confirm the slug is taken.
//     An admin reads and edits a draft through the admin surface, never through this gate.
//   - auth_required: the page is published but for participants only. An anonymous caller is sent to
//     log in; any authenticated caller may read it.
//
// This is total and pure, and it is the whole gate: removing either check here is a policy change a
// test must catch, which is why it is one function and not two ifs inlined in a handler.
func PageView(draft, authRequired bool, pr Principal) Outcome {
	if draft {
		return NotFound
	}
	if authRequired && !pr.Authed {
		return AuthRequired
	}
	return Allow
}
