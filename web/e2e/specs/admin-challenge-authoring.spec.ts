import { test, expect } from "../fixtures";

// The whole authoring path in one browser session: reach the editor from the board, create a
// challenge, add a flag and a hint, hide it and unhide it, upload a file — and after every write,
// reload and assert the fact came back from the server, not from a stale tab.
//
// On current main this fails at the very first hurdle: the admin board has no <Outlet/>, so "New
// challenge" re-renders the board in place of the editor and the "Create challenge" button never
// appears. With the fix the editor is reachable and every step round-trips.
test("author a challenge end to end from the admin board", async ({ adminPage: page }) => {
  const stamp = Date.now();
  const name = `E2E Challenge ${stamp}`;
  const flag = `flag{e2e_${stamp}}`;
  const hintTitle = `e2e-hint-${stamp}`;
  const fileName = `e2e-${stamp}.txt`;

  const header = page.locator(".page-head");
  const openTab = (label: string) => page.getByRole("tab", { name: label }).click();

  // ---- reach the editor from the board (the N1 regression) ----
  await page.goto("/admin/challenges");
  await expect(page.getByRole("heading", { name: "Challenges" })).toBeVisible();
  await page.getByRole("link", { name: "New challenge" }).first().click();
  await expect(page.getByRole("button", { name: "Create challenge" })).toBeVisible();

  // ---- create ----
  await page.getByRole("textbox", { name: "Name", exact: true }).fill(name);
  await page.getByRole("textbox", { name: "Category", exact: true }).fill("rev");
  await page.getByRole("spinbutton", { name: "Value", exact: true }).fill("250");
  await page.getByRole("button", { name: "Create challenge" }).click();
  // The create navigates to the challenge's own URL; the Save button and the title prove it landed.
  await expect(page.getByRole("button", { name: "Save", exact: true })).toBeVisible();
  await expect(header.getByRole("heading", { name })).toBeVisible();
  const editorUrl = page.url();
  expect(editorUrl).not.toContain("/new");

  // ---- add a flag ----
  await openTab("Flags");
  await page.getByRole("textbox", { name: "Flag", exact: true }).fill(flag);
  await page.getByRole("button", { name: "Add flag" }).click();
  await expect(page.getByText(flag)).toBeVisible();
  // reload → the flag is read back from the admin detail, not from this tab's memory.
  await page.reload();
  await openTab("Flags");
  await expect(page.getByText(flag)).toBeVisible();

  // ---- add a hint ----
  await openTab("Hints");
  await page.getByRole("textbox", { name: "Title", exact: true }).fill(hintTitle);
  await page.getByRole("textbox", { name: "Body", exact: true }).fill("look at the entropy");
  await page.getByRole("button", { name: "Add hint" }).click();
  await expect(page.getByRole("cell", { name: hintTitle })).toBeVisible();
  await page.reload();
  await openTab("Hints");
  await expect(page.getByRole("cell", { name: hintTitle })).toBeVisible();

  // ---- hide, then unhide ----
  await openTab("Details");
  await page.getByRole("combobox", { name: "State" }).selectOption("hidden");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(header.getByText("hidden", { exact: true })).toBeVisible();
  await page.reload();
  await expect(header.getByText("hidden", { exact: true })).toBeVisible();

  await openTab("Details");
  await page.getByRole("combobox", { name: "State" }).selectOption("visible");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(header.getByText("published", { exact: true })).toBeVisible();
  await page.reload();
  await expect(header.getByText("published", { exact: true })).toBeVisible();

  // ---- upload a file ----
  await openTab("Files");
  await page.getByLabel("File to attach").setInputFiles({
    name: fileName,
    mimeType: "text/plain",
    buffer: Buffer.from("e2e attachment contents"),
  });
  await page.getByRole("button", { name: "Upload" }).click();
  await expect(page.getByRole("cell", { name: fileName })).toBeVisible();
  await page.reload();
  await openTab("Files");
  await expect(page.getByRole("cell", { name: fileName })).toBeVisible();
});
